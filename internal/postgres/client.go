package postgres

import (
    "context"
    "fmt"
    "log"
    "strings"

    "github.com/Dataloh/pg-ldap-sync/internal/config"
    "github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5"
)

// Client is a wrapper around the PostgreSQL connection pool.
type Client struct {
    Pool   *pgxpool.Pool
    config config.PostgresConn
}

// NewClient creates a new PostgreSQL client.
func NewClient(cfg config.PostgresConn) *Client {
    return &Client{
        config: cfg,
    }
}

// Connect establishes a connection pool to the PostgreSQL server.
func (c *Client) Connect(ctx context.Context) error {
    connString := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
        c.config.User, c.config.Password, c.config.Host, c.config.Port, c.config.DBName, c.config.SSLMode)

    pool, err := pgxpool.New(ctx, connString)
    if err != nil {
        return fmt.Errorf("unable to create connection pool: %w", err)
    }

    // Ping the database to verify the connection.
    if err := pool.Ping(ctx); err != nil {
        return fmt.Errorf("unable to connect to database: %w", err)
    }

    c.Pool = pool
    log.Printf("Successfully connected to PostgreSQL database: %s", c.config.DBName)
    return nil
}

// Close gracefully terminates the connection pool.
func (c *Client) Close() {
    if c.Pool != nil {
        c.Pool.Close()
        log.Println("PostgreSQL connection pool closed.")
    }
}

// EnsureUsersExist creates any missing user roles in a single transaction.
// This is Phase 1 of the synchronization process.
func (c *Client) EnsureUsersExist(ctx context.Context, users []string, defaultGroup string) error {
    tx, err := c.Pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("failed to begin user creation transaction: %w", err)
    }
    defer tx.Rollback(ctx)

    for _, user := range users {
        var exists bool
        err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname = $1)", user).Scan(&exists)
        if err != nil {
            return fmt.Errorf("failed to check for existence of role '%s': %w", user, err)
        }

        if !exists {
            log.Printf("    CREATING user role: %s", user)
            if _, err := tx.Exec(ctx, fmt.Sprintf("CREATE ROLE %s WITH LOGIN;", pgxQuoteIdentifier(user))); err != nil {
                return fmt.Errorf("failed to create user role '%s': %w", user, err)
            }
            log.Printf("    GRANTING default group %s -> %s", defaultGroup, user)
            if _, err := tx.Exec(ctx, fmt.Sprintf("GRANT %s TO %s;", pgxQuoteIdentifier(defaultGroup), pgxQuoteIdentifier(user))); err != nil {
                return fmt.Errorf("failed to grant default role '%s' to new user '%s': %w", defaultGroup, user, err)
            }
        }
    }
    return tx.Commit(ctx)
}

// SyncRoleMembership now ONLY manages memberships between pre-existing roles.
// This is Phase 2 of the synchronization process.
func (c *Client) SyncRoleMembership(ctx context.Context, pgRole string, ldapMembers []string, prefixes []string) error {
    if len(prefixes) == 0 {
        log.Printf("WARNING: Membership sync for role '%s' skipped because no 'AllowedUserPrefixes' are configured.", pgRole)
        return nil
    }

    tx, err := c.Pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("failed to begin transaction: %w", err)
    }
    defer tx.Rollback(ctx)

    // --- Step 1: Get current MANAGED members of the role from Postgres ---
    var whereClauses []string
    args := []interface{}{pgRole} // $1 will be the pgRole

    for i, prefix := range prefixes {
        whereClauses = append(whereClauses, fmt.Sprintf("u.rolname LIKE $%d", i+2))
        args = append(args, prefix+"%")
    }

    query := fmt.Sprintf(`
        SELECT u.rolname
        FROM pg_catalog.pg_roles u
        JOIN pg_catalog.pg_auth_members m ON (m.member = u.oid)
        JOIN pg_catalog.pg_roles g ON (g.oid = m.roleid)
        WHERE g.rolname = $1 AND (%s)`, strings.Join(whereClauses, " OR "))

    rows, err := tx.Query(ctx, query, args...)
    if err != nil {
        return fmt.Errorf("failed to query for current managed members of role '%s': %w", pgRole, err)
    }
    pgManagedMembers, err := pgx.CollectRows(rows, pgx.RowTo[string])
    if err != nil {
        return fmt.Errorf("failed to collect current managed members for role '%s': %w", pgRole, err)
    }

    // --- Step 2: Calculate the difference ---
    ldapMemberSet := make(map[string]bool, len(ldapMembers))
    for _, member := range ldapMembers {
        ldapMemberSet[member] = true
    }

    pgMemberSet := make(map[string]bool, len(pgManagedMembers))
    for _, member := range pgManagedMembers {
        pgMemberSet[member] = true
    }

    var usersToGrant []string
    var usersToRevoke []string

    for _, ldapUser := range ldapMembers {
        if !pgMemberSet[ldapUser] {
            usersToGrant = append(usersToGrant, ldapUser)
        }
    }

    for _, pgUser := range pgManagedMembers {
        if !ldapMemberSet[pgUser] {
            usersToRevoke = append(usersToRevoke, pgUser)
        }
    }

    // --- Step 3: Execute GRANT and REVOKE statements ---
    // Use pgx.Identifier to safely quote all role and user names.
    pgRoleIdentifier := pgx.Identifier{pgRole}

    if len(usersToGrant) > 0 {
        log.Printf("    GRANTING %s -> %v", pgRole, usersToGrant)
        for _, user := range usersToGrant {
            grantSQL := fmt.Sprintf("GRANT %s TO %s", pgRoleIdentifier.Sanitize(), pgx.Identifier{user}.Sanitize())
            if _, err := tx.Exec(ctx, grantSQL); err != nil {
                return fmt.Errorf("failed to grant role '%s' to '%s': %w", pgRole, user, err)
            }
        }
    }

    if len(usersToRevoke) > 0 {
        log.Printf("    REVOKING %s <- %v", pgRole, usersToRevoke)
        for _, user := range usersToRevoke {
            revokeSQL := fmt.Sprintf("REVOKE %s FROM %s", pgRoleIdentifier.Sanitize(), pgx.Identifier{user}.Sanitize())
            if _, err := tx.Exec(ctx, revokeSQL); err != nil {
                return fmt.Errorf("failed to revoke role '%s' from '%s': %w", pgRole, user, err)
            }
        }
    }

    return tx.Commit(ctx)
}

// DeprovisionUsers removes users who are no longer in any valid LDAP groups.
// This is Phase 3 of the synchronization process.
func (c *Client) DeprovisionUsers(ctx context.Context, ldapUsers map[string]bool, groupName string, prefixes []string) error {
    if len(prefixes) == 0 {
        // Safety check: If no prefixes are defined, do nothing to avoid accidentally wiping users.
        log.Println("WARNING: Deprovisioning skipped because no 'AllowedUserPrefixes' are configured.")
        return nil
    }

    tx, err := c.Pool.Begin(ctx)
    if err != nil {
        return fmt.Errorf("failed to begin transaction: %w", err)
    }
    defer tx.Rollback(ctx) // Rollback on any error

    // Build the dynamic query to find all users in the target group THAT MATCH A MANAGED PREFIX.
    var whereClauses []string
    args := []interface{}{groupName} // $1 will be the groupName

    for i, prefix := range prefixes {
        // Use LIKE with a wildcard to match the prefix.
        whereClauses = append(whereClauses, fmt.Sprintf("u.rolname LIKE $%d", i+2))
        args = append(args, prefix+"%")
    }
    
    query := fmt.Sprintf(`
        SELECT u.rolname
        FROM pg_catalog.pg_roles u
        JOIN pg_catalog.pg_auth_members m ON (m.member = u.oid)
        JOIN pg_catalog.pg_roles g ON (g.oid = m.roleid)
        WHERE g.rolname = $1 AND (%s)`, strings.Join(whereClauses, " OR "))

    // Fetch the managed users from Postgres that are candidates for deletion.
    rows, err := tx.Query(ctx, query, args...)
    if err != nil {
        return fmt.Errorf("failed to query for managed postgres users: %w", err)
    }
    
    pgManagedUsers, err := pgx.CollectRows(rows, pgx.RowTo[string])
    if err != nil {
        return fmt.Errorf("failed to collect managed user rows: %w", err)
    }

    // Determine which of the managed users no longer exist in LDAP.
    var usersToDrop []string
    for _, pgUser := range pgManagedUsers {
        if _, existsInLdap := ldapUsers[pgUser]; !existsInLdap {
            usersToDrop = append(usersToDrop, pgUser)
        }
    }

    if len(usersToDrop) == 0 {
        log.Println("No stale users to deprovision.")
        return tx.Commit(ctx) // Nothing to do, commit the (empty) transaction.
    }

    // Execute DROP ROLE commands for each user to be removed.
    log.Printf("Deprovisioning the following stale users: %v", usersToDrop)
    for _, user := range usersToDrop {
        // pgx.Identifier safely quotes the username to prevent SQL injection.
        dropUserSQL := fmt.Sprintf("DROP ROLE %s", pgx.Identifier{user}.Sanitize())
        if _, err := tx.Exec(ctx, dropUserSQL); err != nil {
            // Log the error but continue trying to drop other users.
            log.Printf("    ERROR: Failed to drop user '%s': %v", user, err)
        } else {
            log.Printf("    SUCCESS: Dropped user '%s'.", user)
        }
    }

    return tx.Commit(ctx)
}

// pgxQuoteIdentifier safely quotes a Postgres identifier to prevent SQL injection.
func pgxQuoteIdentifier(name string) string {
    return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

