// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds all runtime settings for the application.
type Config struct {
	Port          string
	DatabaseURL   string
	SessionSecret string
	// EncryptionSecret derives the AES key for data-at-rest (TOTP secrets). It
	// defaults to SessionSecret for backward compatibility, but can be set
	// independently so SessionSecret may be rotated/lengthened without changing
	// the encryption key (which would make stored TOTP secrets undecryptable).
	EncryptionSecret string
	CookieSecure     bool
	TrustProxy       bool
	AdminUsername    string
	AdminPassword    string
	// AdminPasswordReset is a deliberate break-glass: when true, the bootstrap
	// resets the existing admin's password to AdminPassword (and revokes sessions).
	// Off by default so a normal restart never reverts a UI-changed password.
	AdminPasswordReset bool
	// BackupStatusFile is the path to the backup status.json, surfaced on the
	// admin Backup panel. Absent/unreadable -> panel shows "not configured".
	BackupStatusFile string
	// BackupEncryptionKey encrypts every backup at rest (AES-256-GCM). It is
	// deliberately separate from SessionSecret/EncryptionSecret so the backup key
	// can be held apart from the running app's secrets. Empty -> backups disabled.
	BackupEncryptionKey string
	// BackupDir is where the scheduled in-app backup writer drops encrypted dumps.
	BackupDir string
	// BackupIntervalHours is the scheduled-backup cadence; BackupKeep is how many
	// encrypted dumps to retain (older ones are pruned).
	BackupIntervalHours int
	BackupKeep          int
	// Optional S3-compatible off-box destination for scheduled backups (3-2-1).
	// Empty S3Endpoint/S3Bucket disables it.
	S3Endpoint  string
	S3Bucket    string
	S3AccessKey string
	S3SecretKey string
	S3Prefix    string
	S3UseSSL    bool
	// WebAuthn (passkeys). RPID is the effective domain (host only, no scheme);
	// RPOrigin is the full origin the browser sees. Both must match the site.
	RPID     string
	RPOrigin string
}

// Load reads configuration from the environment and validates required values.
func Load() (*Config, error) {
	c := &Config{
		Port:                getenv("APP_PORT", "8080"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		SessionSecret:       os.Getenv("SESSION_SECRET"),
		CookieSecure:        strings.EqualFold(getenv("COOKIE_SECURE", "false"), "true"),
		TrustProxy:          strings.EqualFold(getenv("TRUST_PROXY", "false"), "true"),
		AdminUsername:       getenv("ADMIN_USERNAME", "admin"),
		AdminPassword:       os.Getenv("ADMIN_PASSWORD"),
		AdminPasswordReset:  strings.EqualFold(getenv("ADMIN_PASSWORD_RESET", "false"), "true"),
		BackupStatusFile:    getenv("BACKUP_STATUS_FILE", "/backups/status.json"),
		BackupEncryptionKey: os.Getenv("BACKUP_ENCRYPTION_KEY"),
		BackupDir:           getenv("BACKUP_DIR", "/backups"),
		BackupIntervalHours: getenvInt("BACKUP_INTERVAL_HOURS", 24),
		BackupKeep:          getenvInt("BACKUP_KEEP", 7),
		S3Endpoint:          os.Getenv("S3_ENDPOINT"),
		S3Bucket:            os.Getenv("S3_BUCKET"),
		S3AccessKey:         os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("S3_SECRET_KEY"),
		S3Prefix:            os.Getenv("S3_PREFIX"),
		S3UseSSL:            strings.EqualFold(getenv("S3_USE_SSL", "true"), "true"),
		RPID:                getenv("RP_ID", "localhost"),
		RPOrigin:            getenv("RP_ORIGIN", "http://localhost:8080"),
	}

	// Data-at-rest encryption key. Defaults to SessionSecret so existing
	// deployments keep decrypting their TOTP secrets; set ENCRYPTION_SECRET to
	// the *previous* SessionSecret before lengthening SESSION_SECRET to migrate
	// safely.
	c.EncryptionSecret = getenv("ENCRYPTION_SECRET", c.SessionSecret)

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.SessionSecret == "" || len(c.SessionSecret) < 32 {
		return nil, fmt.Errorf("SESSION_SECRET is required and must be at least 32 characters (e.g. `openssl rand -hex 32`)")
	}
	if len(c.EncryptionSecret) < 16 {
		return nil, fmt.Errorf("ENCRYPTION_SECRET must be at least 16 characters")
	}
	if c.AdminPassword == "" {
		return nil, fmt.Errorf("ADMIN_PASSWORD is required to bootstrap the admin user")
	}
	// Backups are optional, but if a key is set it must be long enough to be a
	// real secret (the AES-256 key is derived from it).
	if c.BackupEncryptionKey != "" && len(c.BackupEncryptionKey) < 16 {
		return nil, fmt.Errorf("BACKUP_ENCRYPTION_KEY must be at least 16 characters")
	}
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
