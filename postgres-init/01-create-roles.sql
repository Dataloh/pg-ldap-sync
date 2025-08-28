    -- This script will be executed automatically by the Postgres container on first start.
    -- It creates the group roles that our sync job will manage memberships for.

    -- \c myapp_db; -- Optional: ensure we are on the right database if needed.

    CREATE ROLE ldap_db_admins WITH NOLOGIN;
    CREATE ROLE ldap_readonly_users WITH NOLOGIN;
    CREATE ROLE g_ldapusers WITH NOLOGIN;

    -- You can add any other initial setup here.

