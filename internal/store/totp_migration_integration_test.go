package store_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestTotpMigrationIntegration proves T-06: a legacy unprefixed (plaintext) TOTP
// seed is re-encrypted to v2 on migration, still decrypts to the same secret, and
// the migration is idempotent.
func TestTotpMigrationIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	uid, err := st.CreateUser(ctx, fmt.Sprintf("t06_%d", os.Getpid()), "pw-xxxxxxxxxxxx", models.RoleEditor)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	// Simulate a very old row: an unprefixed plaintext seed stored directly.
	const plain = "JBSWY3DPEHPK3PXP"
	if _, err := pool.ExecContext(ctx, `UPDATE users SET totp_secret=$2 WHERE id=$1`, uid, plain); err != nil {
		t.Fatalf("seed plaintext: %v", err)
	}

	n, err := st.MigrateTotpSecretsToV2(ctx)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if n < 1 {
		t.Fatalf("expected at least one seed migrated, got %d", n)
	}

	// Stored value is now v2-prefixed (no longer plaintext at rest).
	var stored string
	if err := pool.QueryRowContext(ctx, `SELECT totp_secret FROM users WHERE id=$1`, uid).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasPrefix(stored, "v2:") {
		t.Fatalf("stored seed not v2-encrypted: %q", stored)
	}
	if stored == plain {
		t.Fatalf("seed still plaintext at rest")
	}

	// It still decrypts to the original secret.
	if got, err := st.GetTotpSecret(ctx, uid); err != nil || got != plain {
		t.Fatalf("decrypt after migrate: got %q err %v, want %q", got, err, plain)
	}

	// Idempotent: a second pass rewrites nothing.
	if n2, err := st.MigrateTotpSecretsToV2(ctx); err != nil || n2 != 0 {
		t.Fatalf("second migration should be a no-op: n=%d err=%v", n2, err)
	}
}
