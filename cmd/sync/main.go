package main

import (
    "context"
    "log"
    "path/filepath"
    "strings"
    "time"
    "os"

    "github.com/Dataloh/pg-ldap-sync/internal/config"
    "github.com/Dataloh/pg-ldap-sync/internal/ldap"
    "github.com/Dataloh/pg-ldap-sync/internal/postgres"
)

func main() {
    log.Println("Starting LDAP to Postgres sync process...")
    ctx := context.Background()

    // --- Configuration Loading ---
    configPath := getConfigPath()
    log.Printf("Loading configuration from: %s", configPath)
    cfg, err := config.Load(configPath)
    if err != nil {
        log.Fatalf("Failed to load configuration: %v", err)
    }
    log.Println("Configuration loaded successfully.")

    // --- LDAP Client Setup ---
    log.Println("Initializing LDAP client...")
    ldapClient := ldap.NewClient(cfg.LDAP)
    if err := ldapClient.Connect(); err != nil {
        log.Fatalf("Failed to connect to LDAP server: %v", err)
    }
    defer ldapClient.Close()

    // --- Main Sync Loop ---
    for _, dbCfg := range cfg.Databases {
        log.Printf("--- Processing database: %s ---", dbCfg.Alias)

        pgClient := postgres.NewClient(dbCfg.Postgres)
        if err := pgClient.Connect(ctx); err != nil {
            log.Fatalf("ERROR: Could not connect to PostgreSQL database '%s': %v. Skipping.", dbCfg.Alias, err)
            continue
        }

        // == Phase 1: User Provisioning ==
        // First, gather all valid users from all configured LDAP groups for this DB.
        allValidLdapUsers := make(map[string]bool)
        allRoleMappings := make(map[string][]string) // Store members for Phase 2

        log.Println("Phase 1: Fetching and filtering all LDAP users...")
        for _, roleMap := range dbCfg.Roles {
            ldapMembers, err := ldapClient.FetchGroupMembers(roleMap.LDAPGroupCN)
            if err != nil {
                log.Printf("    ERROR fetching LDAP members for '%s': %v", roleMap.LDAPGroupCN, err)
                continue
            }

            var filteredMembers []string
            for _, member := range ldapMembers {
                isValid := false
                for _, prefix := range cfg.SyncPolicy.AllowedUserPrefixes {
                    if strings.HasPrefix(member, prefix) {
                        isValid = true
                        break
                    }
                }
                if isValid {
                    filteredMembers = append(filteredMembers, member)
                    allValidLdapUsers[member] = true
                }
            }
            allRoleMappings[roleMap.PostgresRole] = filteredMembers
        }

        // Convert map keys to a slice for the creation function.
        var usersToCreate []string
        for user := range allValidLdapUsers {
            usersToCreate = append(usersToCreate, user)
        }

        // Now, run a single transaction to create all missing users.
        provCtx, cancelProv := context.WithTimeout(ctx, 60*time.Second)
        log.Println("Phase 1: Ensuring all valid users exist in PostgreSQL...")
        if err := pgClient.EnsureUsersExist(provCtx, usersToCreate, cfg.SyncPolicy.DefaultPostgresGroup); err != nil {
            log.Fatalf("ERROR during user provisioning phase: %v. Skipping membership sync for this DB.", err)
            cancelProv()
            pgClient.Close()
            continue
        }
        cancelProv()
        log.Println("Phase 1: User provisioning complete.")

        // == Phase 2: Membership Sync ==
        log.Println("Phase 2: Synchronizing group memberships...")
        for pgRole, members := range allRoleMappings {
            log.Printf("--> Syncing membership for: [%s]", pgRole)
            syncCtx, cancelSync := context.WithTimeout(ctx, 30*time.Second)
            err = pgClient.SyncRoleMembership(syncCtx, pgRole, members, cfg.SyncPolicy.AllowedUserPrefixes)
            if err != nil {
                log.Fatalf("    ERROR: Failed to sync role membership: %v", err)
            } else {
                log.Printf("    SUCCESS: PostgreSQL role '%s' is synchronized.", pgRole)
            }
            cancelSync()
        }
        log.Println("Phase 2: Membership sync complete.")

        // == Phase 3: Deprovisioning ==
        deprovisionCtx, cancelDeprov := context.WithTimeout(ctx, 30*time.Second)
        if err := pgClient.DeprovisionUsers(deprovisionCtx, allValidLdapUsers, cfg.SyncPolicy.DefaultPostgresGroup, cfg.SyncPolicy.AllowedUserPrefixes); err != nil {
            log.Fatalf("ERROR: Failed to deprovision users: %v", err)
        } else {
            log.Println("Phase 3: Deprovisioning complete.")
        }
        cancelDeprov()

        pgClient.Close()
    }

    log.Println("Sync process finished.")
}

// getConfigPath determines the path to the config.yml file.
func getConfigPath() string {
	if cfgPath := os.Getenv("CFG_PATH"); cfgPath != "" {
		configPath, err := filepath.Abs(cfgPath)
		if err != nil {
			log.Fatalf("Cannot determine absolute path for config file: %v", err)
		}

		return configPath
	} 
	configPath, err := filepath.Abs("/opt/pg-ldap-sync/config.yml")
	if err != nil {
		log.Fatalf("Cannot determine absolute path for config file: %v", err)
	}

    return configPath
}

