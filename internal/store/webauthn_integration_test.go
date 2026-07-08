package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"treckrr/internal/db"
	"treckrr/internal/models"
	"treckrr/internal/store"
)

// TestWebauthnBackupFlagsRoundTrip proves the BE/BS flags survive the DB
// round-trip. Storing BackupEligible is required so login can replay it —
// go-webauthn rejects a credential whose stored BE flag differs from the
// authenticator's assertion. Runs only when TEST_DATABASE_URL is set.
func TestWebauthnBackupFlagsRoundTrip(t *testing.T) {
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

	// Unique username per run + cleanup, so reruns against a persistent
	// TEST_DATABASE_URL don't collide on the username unique constraint. The
	// defer runs before pool.Close() (LIFO); deleting the user cascades to its
	// credentials.
	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	username := "wa-flags-" + hex.EncodeToString(suffix)
	uid, err := st.CreateUser(ctx, username, "pw-at-least-8-chars", models.RoleAdmin)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	defer func() {
		if _, err := pool.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, uid); err != nil {
			t.Errorf("cleanup user: %v", err)
		}
	}()

	credID := make([]byte, 16)
	_, _ = rand.Read(credID)
	in := models.WebauthnCredential{
		CredentialID:   credID,
		PublicKey:      []byte("pubkey"),
		AAGUID:         make([]byte, 16),
		SignCount:      0,
		Transports:     "internal,hybrid",
		Name:           "Synced (Bitwarden-like)",
		BackupEligible: true, // synced authenticator presents BE=1
		BackupState:    true,
	}
	if err := st.AddWebauthnCredential(ctx, uid, in); err != nil {
		t.Fatalf("add credential: %v", err)
	}

	creds, err := st.ListWebauthnCredentials(ctx, uid)
	if err != nil || len(creds) != 1 {
		t.Fatalf("list credentials: %v (n=%d)", err, len(creds))
	}
	got := creds[0]
	if !got.BackupEligible {
		t.Fatalf("BackupEligible did not round-trip: got false, want true")
	}
	if !got.BackupState {
		t.Fatalf("BackupState did not round-trip: got false, want true")
	}
}
