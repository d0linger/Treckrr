// Package config loads runtime configuration from environment variables.
package config

import (
	"fmt"
	"net"
	netmail "net/mail"
	"os"
	"strconv"
	"strings"
)

// Documented Compose placeholders that must never reach a running instance
// (T-01): they satisfy the presence/length checks but leave the signing/
// encryption key and the admin login publicly known.
const (
	placeholderSessionSecret = "change-me-please-generate-a-random-value"
	placeholderAdminPassword = "change-me-admin-password"
	// The Compose default for the database. It reaches the app inside
	// DATABASE_URL rather than as its own variable, which is why it slipped past
	// the placeholder checks above — but a publicly known database password is
	// the same class of problem as a known admin password.
	// #nosec G101 -- this is the published placeholder the check below REJECTS,
	// not a credential the application ever uses.
	placeholderDBPassword = "treckrr-db-password-change-me"
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
	// TrustedProxies, when set via TRUSTED_PROXIES (comma-separated CIDRs),
	// restricts TRUST_PROXY so forwarded headers are only honored when the direct
	// peer (RemoteAddr) is inside one of these networks (SH-05). Empty keeps the
	// legacy behavior: TRUST_PROXY alone trusts the forwarded header.
	TrustedProxies []*net.IPNet
	AdminUsername  string
	AdminPassword  string
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
	// BackupKeep is how many encrypted dumps to retain (older ones are pruned).
	// The schedule itself is cron-based and configured in the admin panel.
	BackupKeep int
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
	// MetricsToken, when set via METRICS_TOKEN, enables GET /metrics (Prometheus
	// text format) guarded by an "Authorization: Bearer <token>" header. Empty
	// (the default) leaves the endpoint unregistered, so metrics are opt-in.
	MetricsToken string

	// SMTP settings enable sending a Beleg/Rechnung by e-mail. Feature is opt-in:
	// e-mail send is offered only when SMTPHost and SMTPFrom are both set.
	SMTPHost     string
	SMTPPort     string
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	SMTPStartTLS bool
}

// MailEnabled reports whether e-mail sending is configured.
func (c *Config) MailEnabled() bool {
	return strings.TrimSpace(c.SMTPHost) != "" && strings.TrimSpace(c.SMTPFrom) != ""
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
		BackupKeep:          getenvInt("BACKUP_KEEP", 7),
		S3Endpoint:          os.Getenv("S3_ENDPOINT"),
		S3Bucket:            os.Getenv("S3_BUCKET"),
		S3AccessKey:         os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:         os.Getenv("S3_SECRET_KEY"),
		S3Prefix:            os.Getenv("S3_PREFIX"),
		S3UseSSL:            strings.EqualFold(getenv("S3_USE_SSL", "true"), "true"),
		RPID:                getenv("RP_ID", "localhost"),
		RPOrigin:            getenv("RP_ORIGIN", "http://localhost:8080"),
		MetricsToken:        os.Getenv("METRICS_TOKEN"),
		SMTPHost:            os.Getenv("SMTP_HOST"),
		SMTPPort:            getenv("SMTP_PORT", "587"),
		SMTPUser:            os.Getenv("SMTP_USER"),
		SMTPPassword:        os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:            os.Getenv("SMTP_FROM"),
		SMTPStartTLS:        strings.EqualFold(getenv("SMTP_STARTTLS", "true"), "true"),
	}

	// Data-at-rest encryption key. Defaults to SessionSecret so existing
	// deployments keep decrypting their TOTP secrets; set ENCRYPTION_SECRET to
	// the *previous* SessionSecret before lengthening SESSION_SECRET to migrate
	// safely.
	c.EncryptionSecret = getenv("ENCRYPTION_SECRET", c.SessionSecret)

	// Optional allowlist of trusted reverse-proxy networks (SH-05). Invalid CIDRs
	// fail fast rather than silently disabling the tightening.
	if raw := strings.TrimSpace(os.Getenv("TRUSTED_PROXIES")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			_, ipnet, err := net.ParseCIDR(part)
			if err != nil {
				return nil, fmt.Errorf("TRUSTED_PROXIES: invalid CIDR %q: %w", part, err)
			}
			c.TrustedProxies = append(c.TrustedProxies, ipnet)
		}
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if strings.Contains(c.DatabaseURL, placeholderDBPassword) {
		// Actionable on purpose: POSTGRES_PASSWORD only takes effect when the data
		// directory is FIRST created, so changing it in .env does nothing to an
		// existing database. Without saying so, the obvious fix appears not to work.
		return nil, fmt.Errorf("DATABASE_URL still contains the documented placeholder database password. " +
			"Set POSTGRES_PASSWORD and DATABASE_URL to a real secret; for a database that already exists, " +
			"also rotate the role itself: docker exec treckrr-db psql -U treckrr -d treckrr " +
			"-c \"ALTER USER treckrr WITH PASSWORD '<new>'\"")
	}
	if c.SessionSecret == "" || len(c.SessionSecret) < 32 {
		return nil, fmt.Errorf("SESSION_SECRET is required and must be at least 32 characters (e.g. `openssl rand -hex 32`)")
	}
	// Refuse the documented Compose placeholders (T-01): they pass the length check
	// but leave signing/encryption material and the admin account publicly known.
	if c.SessionSecret == placeholderSessionSecret {
		return nil, fmt.Errorf("SESSION_SECRET is still the documented placeholder — set a real secret (e.g. `openssl rand -hex 32`)")
	}
	if len(c.EncryptionSecret) < 16 {
		return nil, fmt.Errorf("ENCRYPTION_SECRET must be at least 16 characters")
	}
	if c.EncryptionSecret == placeholderSessionSecret {
		return nil, fmt.Errorf("ENCRYPTION_SECRET is still the documented placeholder — set a real secret")
	}
	if c.AdminPassword == "" {
		return nil, fmt.Errorf("ADMIN_PASSWORD is required to bootstrap the admin user")
	}
	if c.AdminPassword == placeholderAdminPassword {
		return nil, fmt.Errorf("ADMIN_PASSWORD is still the documented placeholder — set a real admin password")
	}
	// Backups are optional (unset = off). If a key is set it must be a real
	// secret: reject a whitespace-only value (it would derive a guessable key)
	// and require real length. The original key bytes are kept untrimmed.
	if strings.TrimSpace(c.BackupEncryptionKey) == "" {
		if c.BackupEncryptionKey != "" {
			return nil, fmt.Errorf("BACKUP_ENCRYPTION_KEY must not be whitespace only (leave it unset to disable backups)")
		}
	} else if len(c.BackupEncryptionKey) < 16 {
		return nil, fmt.Errorf("BACKUP_ENCRYPTION_KEY must be at least 16 characters")
	}
	// A configured sender must be a well-formed address, or every send fails at
	// runtime with an opaque error. Blank stays valid (e-mail is simply disabled).
	if from := strings.TrimSpace(c.SMTPFrom); from != "" {
		if _, err := netmail.ParseAddress(from); err != nil {
			return nil, fmt.Errorf("SMTP_FROM is not a valid e-mail address: %w", err)
		}
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
