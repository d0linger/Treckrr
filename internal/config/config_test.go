package config

import (
	"strings"
	"testing"
)

// TestLoadRejectsPlaceholders proves T-01: the documented Compose placeholder
// secrets are refused even though they pass the presence/length checks, while a
// real configuration loads.
func TestLoadRejectsPlaceholders(t *testing.T) {
	setValid := func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://user:pw@db:5432/treckrr?sslmode=disable")
		t.Setenv("SESSION_SECRET", strings.Repeat("a", 40))
		t.Setenv("ADMIN_PASSWORD", "a-real-admin-password")
	}

	t.Run("valid config loads", func(t *testing.T) {
		setValid(t)
		if _, err := Load(); err != nil {
			t.Fatalf("valid config should load: %v", err)
		}
	})

	t.Run("placeholder SESSION_SECRET is rejected", func(t *testing.T) {
		setValid(t)
		t.Setenv("SESSION_SECRET", placeholderSessionSecret)
		if _, err := Load(); err == nil {
			t.Fatal("expected an error for the placeholder SESSION_SECRET")
		}
	})

	t.Run("placeholder ADMIN_PASSWORD is rejected", func(t *testing.T) {
		setValid(t)
		t.Setenv("ADMIN_PASSWORD", placeholderAdminPassword)
		if _, err := Load(); err == nil {
			t.Fatal("expected an error for the placeholder ADMIN_PASSWORD")
		}
	})

	t.Run("invalid TRUSTED_PROXIES CIDR is rejected", func(t *testing.T) {
		setValid(t)
		t.Setenv("TRUSTED_PROXIES", "not-a-cidr")
		if _, err := Load(); err == nil {
			t.Fatal("expected an error for an invalid TRUSTED_PROXIES CIDR")
		}
	})
}
