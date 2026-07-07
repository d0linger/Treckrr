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
	CookieSecure  bool
	TrustProxy    bool
	AdminUsername string
	AdminPassword string
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

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.SessionSecret == "" || len(c.SessionSecret) < 16 {
		return nil, fmt.Errorf("SESSION_SECRET is required and must be at least 16 characters")
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
