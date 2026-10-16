package config

import (
	"strings"
	"testing"
)

// TestDemoModeDisablesOutboundServices proves a practice instance cannot send
// mail, write to an S3 bucket or run rehearsals even when real credentials were
// copied into its environment, while the same environment without DEMO_MODE
// keeps every service.
func TestDemoModeDisablesOutboundServices(t *testing.T) {
	setOutbound := func(t *testing.T) {
		t.Setenv("DATABASE_URL", "postgres://user:pw@db:5432/treckrr?sslmode=disable")
		t.Setenv("SESSION_SECRET", strings.Repeat("a", 40))
		t.Setenv("ADMIN_PASSWORD", "a-real-admin-password-1")
		t.Setenv("COOKIE_SECURE", "true")
		t.Setenv("TRUST_PROXY", "false")
		t.Setenv("TRUSTED_PROXIES", "")
		t.Setenv("ALLOW_INSECURE_HTTP", "false")
		t.Setenv("SMTP_HOST", "smtp.example.invalid")
		t.Setenv("SMTP_FROM", "hof@example.invalid")
		t.Setenv("SMTP_USER", "hof")
		t.Setenv("SMTP_PASSWORD", "smtp-secret")
		t.Setenv("S3_ENDPOINT", "s3.example.invalid")
		t.Setenv("S3_BUCKET", "backups")
		t.Setenv("S3_PREFIX", "farm-a")
		t.Setenv("S3_ACCESS_KEY", "key")
		t.Setenv("S3_SECRET_KEY", "secret")
		t.Setenv("BACKUP_ENCRYPTION_KEY", strings.Repeat("k", 32))
		t.Setenv("BACKUP_REHEARSE_URL", "postgres://user:pw@db:5432/postgres")
		t.Setenv("BACKUP_REHEARSE_CRON", "0 3 * * 0")
		t.Setenv("UPDATE_CHECK", "true")
	}

	t.Run("live instance keeps outbound services", func(t *testing.T) {
		setOutbound(t)
		t.Setenv("DEMO_MODE", "")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if c.DemoMode || !c.MailEnabled() || c.S3Endpoint == "" || c.BackupRehearseURL == "" {
			t.Fatalf("live config lost outbound services: %+v", c)
		}
	})

	t.Run("practice instance drops them", func(t *testing.T) {
		setOutbound(t)
		t.Setenv("DEMO_MODE", "true")
		c, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if !c.DemoMode {
			t.Fatal("DEMO_MODE=true not recognized")
		}
		if c.MailEnabled() || c.SMTPPassword != "" || c.SMTPUser != "" {
			t.Fatal("practice instance can still send mail")
		}
		if c.S3Endpoint != "" || c.S3Bucket != "" || c.S3AccessKey != "" || c.S3SecretKey != "" {
			t.Fatal("practice instance can still reach the S3 bucket")
		}
		if c.BackupRehearseURL != "" || c.BackupRehearseCron != "" {
			t.Fatal("practice instance can still run restore rehearsals")
		}
		if c.UpdateCheck {
			t.Fatal("practice instance can still call out for updates")
		}
	})
}
