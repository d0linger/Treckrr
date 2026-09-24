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
		t.Setenv("ADMIN_PASSWORD", "a-real-admin-password-1")
		t.Setenv("COOKIE_SECURE", "true")
		t.Setenv("TRUST_PROXY", "false")
		t.Setenv("TRUSTED_PROXIES", "")
		t.Setenv("ALLOW_INSECURE_HTTP", "false")
	}

	t.Run("valid config loads", func(t *testing.T) {
		setValid(t)
		if _, err := Load(); err != nil {
			t.Fatalf("valid config should load: %v", err)
		}
	})

	t.Run("negative S3 retention is rejected", func(t *testing.T) {
		setValid(t)
		t.Setenv("S3_KEEP", "-1")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "S3_KEEP") {
			t.Fatalf("expected S3_KEEP validation error, got %v", err)
		}
	})

	t.Run("S3 requires a complete destination", func(t *testing.T) {
		setValid(t)
		t.Setenv("S3_ENDPOINT", "s3.example.invalid")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "S3_BUCKET") {
			t.Fatalf("expected incomplete S3 destination error, got %v", err)
		}
	})

	t.Run("S3 requires an installation prefix", func(t *testing.T) {
		setValid(t)
		t.Setenv("S3_ENDPOINT", "s3.example.invalid")
		t.Setenv("S3_BUCKET", "backups")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "S3_PREFIX") {
			t.Fatalf("expected S3_PREFIX error, got %v", err)
		}
	})

	t.Run("S3 prefix is normalized", func(t *testing.T) {
		setValid(t)
		t.Setenv("S3_ENDPOINT", "s3.example.invalid")
		t.Setenv("S3_BUCKET", "backups")
		t.Setenv("S3_PREFIX", `/farm-a/production/`)
		cfg, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.S3Prefix != "farm-a/production/" {
			t.Fatalf("S3Prefix = %q", cfg.S3Prefix)
		}
		if cfg.S3LegacyPrefix != `/farm-a/production/` {
			t.Fatalf("S3LegacyPrefix = %q", cfg.S3LegacyPrefix)
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

	t.Run("placeholder ENCRYPTION_SECRET is rejected", func(t *testing.T) {
		setValid(t)
		t.Setenv("ENCRYPTION_SECRET", placeholderSessionSecret)
		if _, err := Load(); err == nil {
			t.Fatal("expected an error for the placeholder ENCRYPTION_SECRET")
		}
	})

	t.Run("invalid TRUSTED_PROXIES CIDR is rejected", func(t *testing.T) {
		setValid(t)
		t.Setenv("TRUSTED_PROXIES", "not-a-cidr")
		if _, err := Load(); err == nil {
			t.Fatal("expected an error for an invalid TRUSTED_PROXIES CIDR")
		}
	})

	t.Run("TRUST_PROXY requires a nonempty allowlist", func(t *testing.T) {
		setValid(t)
		t.Setenv("TRUST_PROXY", "true")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
			t.Fatalf("expected TRUST_PROXY allowlist error, got %v", err)
		}
	})

	t.Run("trusted proxy configuration loads", func(t *testing.T) {
		setValid(t)
		t.Setenv("TRUST_PROXY", "true")
		t.Setenv("TRUSTED_PROXIES", "10.0.0.5/32")
		if _, err := Load(); err != nil {
			t.Fatalf("trusted proxy configuration should load: %v", err)
		}
	})

	t.Run("trusted proxy still requires secure cookies", func(t *testing.T) {
		setValid(t)
		t.Setenv("COOKIE_SECURE", "false")
		t.Setenv("TRUST_PROXY", "true")
		t.Setenv("TRUSTED_PROXIES", "10.0.0.5/32")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ALLOW_INSECURE_HTTP") {
			t.Fatalf("expected secure cookie error, got %v", err)
		}
	})

	t.Run("plain HTTP requires explicit development opt-in", func(t *testing.T) {
		setValid(t)
		t.Setenv("COOKIE_SECURE", "false")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ALLOW_INSECURE_HTTP") {
			t.Fatalf("expected insecure HTTP opt-in error, got %v", err)
		}
	})

	t.Run("explicit direct HTTP development configuration loads", func(t *testing.T) {
		setValid(t)
		t.Setenv("COOKIE_SECURE", "false")
		t.Setenv("ALLOW_INSECURE_HTTP", "true")
		cfg, err := Load()
		if err != nil {
			t.Fatalf("explicit insecure development configuration should load: %v", err)
		}
		if !cfg.AllowInsecureHTTP || cfg.CookieSecure || cfg.TrustProxy {
			t.Fatalf("unexpected insecure development configuration: %+v", cfg)
		}
	})

	t.Run("insecure HTTP cannot be combined with proxy trust", func(t *testing.T) {
		setValid(t)
		t.Setenv("COOKIE_SECURE", "false")
		t.Setenv("ALLOW_INSECURE_HTTP", "true")
		t.Setenv("TRUST_PROXY", "true")
		t.Setenv("TRUSTED_PROXIES", "10.0.0.5/32")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "only valid") {
			t.Fatalf("expected insecure/proxy conflict error, got %v", err)
		}
	})

	t.Run("weak legacy ADMIN_PASSWORD is deferred to the write path", func(t *testing.T) {
		setValid(t)
		t.Setenv("ADMIN_PASSWORD", "weak-password")
		if _, err := Load(); err != nil {
			t.Fatalf("unused legacy ADMIN_PASSWORD blocked startup: %v", err)
		}
	})
}
