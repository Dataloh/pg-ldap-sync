// internal/ldap/client.go

package ldap

import (
    "crypto/tls"
    "crypto/x509"
    "fmt"
    "log"
    "os"
    "strings"

    "github.com/Dataloh/pg-ldap-sync/internal/config"
    "github.com/go-ldap/ldap/v3"
)

type Client struct {
    Conn   *ldap.Conn
    config config.LDAPConfig
}

func NewClient(cfg config.LDAPConfig) *Client {
    return &Client{
        config: cfg,
    }
}

func (c *Client) Connect() error {
    address := fmt.Sprintf("%s:%d", c.config.Host, c.config.Port)

    if !c.config.UseTLS {
        conn, err := ldap.Dial("tcp", address)
        if err != nil {
            return fmt.Errorf("failed to dial LDAP server: %w", err)
        }
        c.Conn = conn
    } else {
        tlsConfig := &tls.Config{
            ServerName: c.config.Host,
        }

        if c.config.CACertPath != "" {
            log.Printf("Loading custom CA from: %s", c.config.CACertPath)
            certPool := x509.NewCertPool()
            ca, err := os.ReadFile(c.config.CACertPath)
            if err != nil {
                return fmt.Errorf("could not read CA certificate from '%s': %w", c.config.CACertPath, err)
            }
            if ok := certPool.AppendCertsFromPEM(ca); !ok {
                return fmt.Errorf("failed to append CA cert from '%s' to pool", c.config.CACertPath)
            }
            tlsConfig.RootCAs = certPool
        } else if c.config.SkipTLSVerify {
            log.Println("Warning: No CA certificate provided. Using InsecureSkipVerify as a fallback.")
            tlsConfig.InsecureSkipVerify = true
        } else {
            return fmt.Errorf("TLS is enabled, but no ca_cert_path was provided and skip_tls_verify is false")
        }

        conn, err := ldap.DialTLS("tcp", address, tlsConfig)
        if err != nil {
            return fmt.Errorf("failed to dial LDAPS server: %w", err)
        }
        c.Conn = conn
    }

    err := c.Conn.Bind(c.config.BindDN, c.config.BindPassword)
    if err != nil {
        return fmt.Errorf("failed to bind to LDAP server: %w", err)
    }

    log.Println("Successfully connected and bound to LDAP server.")
    return nil
}

func (c *Client) Close() {
    if c.Conn != nil {
        c.Conn.Close()
        log.Println("LDAP connection closed.")
    }
}

// FetchGroupMembers is the public entry point for fetching all users from a group, including nested groups.
func (c *Client) FetchGroupMembers(groupCN string) ([]string, error) {
    // First, find the full Distinguished Name (DN) of the starting group.
    groupDN, err := c.findGroupDN(groupCN)
    if err != nil {
        return nil, err
    }

    // These maps will be populated by the recursive search.
    // userIDs stores the final list of user UIDs, using a map to prevent duplicates.
    userIDs := make(map[string]bool)
    // processedGroups prevents infinite loops from circular group memberships.
    processedGroups := make(map[string]bool)

    // Start the recursive search.
    log.Printf("Starting recursive member search for group: %s", groupCN)
    if err := c.fetchMembersRecursive(groupDN, userIDs, processedGroups); err != nil {
        return nil, fmt.Errorf("recursive search failed for group '%s': %w", groupCN, err)
    }

    // Convert the map keys to a slice for the return value.
    finalUserList := make([]string, 0, len(userIDs))
    for uid := range userIDs {
        finalUserList = append(finalUserList, uid)
    }

    log.Printf("Found %d unique members in group '%s' and its subgroups.", len(finalUserList), groupCN)
    return finalUserList, nil
}

// fetchMembersRecursive performs the actual work of expanding group memberships.
func (c *Client) fetchMembersRecursive(groupDN string, userIDs map[string]bool, processedGroups map[string]bool) error {
    // --- Loop prevention ---
    if processedGroups[groupDN] {
        log.Printf("    (Skipping already processed group: %s)", groupDN)
        return nil
    }
    processedGroups[groupDN] = true

    // Search for the current group object to get its members.
    searchRequest := ldap.NewSearchRequest(
        groupDN,
        ldap.ScopeBaseObject, // We are looking at this specific group object.
        ldap.NeverDerefAliases,
        0, 0, false,
        "(objectClass=*)", // A filter that will always match the object.
        []string{"member"},
        nil,
    )

    sr, err := c.Conn.Search(searchRequest)
    if err != nil {
        return fmt.Errorf("LDAP search for group DN '%s' failed: %w", groupDN, err)
    }
    if len(sr.Entries) == 0 {
        return fmt.Errorf("could not find group object for DN '%s'", groupDN)
    }

    memberDNs := sr.Entries[0].GetAttributeValues("member")
    if len(memberDNs) == 0 {
        return nil // Group is empty, nothing more to do here.
    }

    // --- Process each member ---
    for _, memberDN := range memberDNs {
        if memberDN == "" {
            continue
        }
        // For each member, we need to find out what it is (a user or a group).
        memberEntry, err := c.getObject(memberDN)
        if err != nil {
            log.Printf("Warning: Could not retrieve object for DN '%s': %v. Skipping.", memberDN, err)
            continue
        }

        objectClasses := memberEntry.GetAttributeValues("objectClass")
        isGroup := false
        for _, oc := range objectClasses {
            // Check if the object is a group. Common objectClasses are 'group', 'groupOfNames'.
            if strings.EqualFold(oc, "group") || strings.EqualFold(oc, "groupOfNames") {
                isGroup = true
                break
            }
        }

        if isGroup {
            // --- RECURSIVE STEP ---
            // If it's a group, recurse into it.
            log.Printf("    -> Found nested group, recursing into: %s", memberDN)
            if err := c.fetchMembersRecursive(memberDN, userIDs, processedGroups); err != nil {
                log.Printf("Warning: failed to process nested group '%s': %v", memberDN, err)
            }
        } else {
            // --- BASE CASE ---
            // If it's not a group, assume it's a user and try to get its 'uid'.
            uid := memberEntry.GetAttributeValue(c.config.UserObjectClass)
            if uid == "" {
                log.Printf("Warning: Member with DN '%s' is not a group and has no '%s' attribute. Skipping.", memberDN, c.config.UserObjectClass)
                continue
            }
            if !userIDs[uid] {
                log.Printf("    -> Found user: %s", uid)
                userIDs[uid] = true
            }
        }
    }
    return nil
}

// findGroupDN locates the full DN of a group given its Common Name (CN).
func (c *Client) findGroupDN(groupCN string) (string, error) {
    searchRequest := ldap.NewSearchRequest(
        c.config.GroupSearchBase,
        ldap.ScopeWholeSubtree,
        ldap.NeverDerefAliases,
        0, 0, false,
        fmt.Sprintf("(&(objectClass=%s)(cn=%s))",c.config.GroupObjectClass, ldap.EscapeFilter(groupCN)),
        []string{"dn"}, // We only need the DN.
        nil,
    )

    sr, err := c.Conn.Search(searchRequest)
    if err != nil {
        return "", fmt.Errorf("LDAP search for group CN '%s' failed: %w", groupCN, err)
    }
    if len(sr.Entries) == 0 {
        return "", fmt.Errorf("LDAP group with CN '%s' not found under search base '%s'", groupCN, c.config.GroupSearchBase)
    }
    if len(sr.Entries) > 1 {
        return "", fmt.Errorf("found multiple LDAP groups with CN '%s'", groupCN)
    }
    return sr.Entries[0].DN, nil
}

// getObject retrieves a full LDAP entry for a given DN.
func (c *Client) getObject(dn string) (*ldap.Entry, error) {
    searchRequest := ldap.NewSearchRequest(
        dn,
        ldap.ScopeBaseObject,
        ldap.NeverDerefAliases,
        0, 0, false,
        "(objectClass=*)",
        []string{"objectClass", c.config.UserObjectClass},
        nil,
    )

    sr, err := c.Conn.Search(searchRequest)
    if err != nil {
        return nil, err
    }
    if len(sr.Entries) == 0 {
        return nil, fmt.Errorf("object not found")
    }
    return sr.Entries[0], nil
}
