package config

import (
	"os"
	"gopkg.in/yaml.v3"
)

// SyncPolicy defines the rules for the synchronization process.
type SyncPolicy struct {
    AllowedUserPrefixes  []string `yaml:"allowed_user_prefixes"`
    DefaultPostgresGroup string   `yaml:"default_postgres_group"`
}

// Config is the top-level configuration struct.
type Config struct {
    SyncPolicy SyncPolicy       `yaml:"sync_policy"` // Add this line
    Databases  []DatabaseConfig `yaml:"databases"`
    LDAP       LDAPConfig       `yaml:"ldap"`
}

// DatabaseConfig holds all settings for a single PostgreSQL instance and its roles.
type DatabaseConfig struct {
	Alias    string       `yaml:"alias"`
	Postgres PostgresConn `yaml:"postgres"`
	Roles    []RoleMap    `yaml:"roles"`
}

// PostgresConn holds the connection details for a PostgreSQL database.
type PostgresConn struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"` // This will be loaded from env
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
}

// RoleMap defines the mapping between a Postgres role and an LDAP group.
type RoleMap struct {
	PostgresRole  string `yaml:"postgres_role"`
	LDAPGroupCN   string `yaml:"ldap_group_cn"`
}

// LDAPConfig holds the settings for connecting to the LDAP server.
type LDAPConfig struct {
	Host              string `yaml:"host"`
	Port              int    `yaml:"port"`
	BindDN            string `yaml:"bind_dn"`
	BindPassword      string `yaml:"bind_password"`
	BaseDN            string `yaml:"base_dn"`
	GroupSearchBase   string `yaml:"group_search_base"`
	UserSearchBase    string `yaml:"user_search_base"`
	UseTLS            bool   `yaml:"use_tls"`
    SkipTLSVerify     bool   `yaml:"skip_tls_verify"`
	CACertPath        string `yaml:"ca_cert_path"`
}

// Load reads the configuration from a YAML file and overrides specific fields
// with environment variables for security and flexibility.
func Load(path string) (*Config, error) {
	f, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(f, &cfg); err != nil {
		return nil, err
	}

	// Override sensitive and environment-specific data from environment variables.
	// This is crucial for secure deployments (e.g., in Kubernetes).
	if pgPassword := os.Getenv("PG_PASSWORD"); pgPassword != "" {
		// Apply the password to all configured databases
		for i := range cfg.Databases {
			cfg.Databases[i].Postgres.Password = pgPassword
		}
	}

	if ldapBindDN := os.Getenv("LDAP_BIND_DN"); ldapBindDN != "" {
		cfg.LDAP.BindDN = ldapBindDN
	}

	if ldapPassword := os.Getenv("LDAP_BIND_PASSWORD"); ldapPassword != "" {
		cfg.LDAP.BindPassword = ldapPassword
	}

	return &cfg, nil
}
