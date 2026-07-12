// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"os"
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
	// WebAuthn (passkeys). RPID is the effective domain (host only, no scheme);
	// RPOrigin is the full origin the browser sees. Both must match the site.
	RPID     string
	RPOrigin string
}

// Load reads configuration from the environment and validates required values.
func Load() (*Config, error) {
	c := &Config{
		Port:          getenv("APP_PORT", "8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		SessionSecret: os.Getenv("SESSION_SECRET"),
		CookieSecure:  strings.EqualFold(getenv("COOKIE_SECURE", "false"), "true"),
		TrustProxy:    strings.EqualFold(getenv("TRUST_PROXY", "false"), "true"),
		AdminUsername: getenv("ADMIN_USERNAME", "admin"),
		AdminPassword: os.Getenv("ADMIN_PASSWORD"),
		RPID:          getenv("RP_ID", "localhost"),
		RPOrigin:      getenv("RP_ORIGIN", "http://localhost:8080"),
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
	return c, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
