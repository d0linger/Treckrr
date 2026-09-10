//go:build integration

package backup_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/db"
)

func TestRestoreOmitsSessionsBeforeReconciliation(t *testing.T) {
	requirePGTools(t)
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "treckrr_restore_test_" + hex.EncodeToString(nonce[:])
	u.Path = "/postgres"
	q := u.Query()
	q.Del("dbname")
	u.RawQuery = q.Encode()
	admin, err := db.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE "`+name+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Errorf("cleanup scratch database: %v", err)
		}
	})
	u.Path = "/" + name
	pool, err := db.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRowContext(t.Context(), `INSERT INTO users (username,password_hash) VALUES ('restore-test','test-only') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), `INSERT INTO sessions (token,user_id,expires_at) VALUES ('archived-session',$1,now()+interval '1 day')`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), `INSERT INTO webauthn_ceremonies (id,session_data,expires_at) VALUES ('archived-challenge','{}',now()+interval '5 minutes')`); err != nil {
		t.Fatal(err)
	}
	const key = "synthetic-backup-restore-test-key"
	svc := backup.New(backup.Options{DatabaseURL: u.String(), EncKey: key, Dir: t.TempDir()}, pool)
	enc, _, err := svc.CreateEncrypted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := backup.DecryptWith(enc, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreRaw(t.Context(), raw, u.String()); err != nil {
		t.Fatal(err)
	}
	// Deliberately no ReconcileAfterRestore call: the restore commit itself
	// must leave ephemeral auth state empty, including on an immediate crash.
	db.ResetPool(pool)
	var sessions, ceremonies, users int
	if err := pool.QueryRowContext(t.Context(), `SELECT (SELECT count(*) FROM sessions),(SELECT count(*) FROM webauthn_ceremonies),(SELECT count(*) FROM users)`).Scan(&sessions, &ceremonies, &users); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || ceremonies != 0 || users != 1 {
		t.Fatalf("sessions=%d ceremonies=%d users=%d", sessions, ceremonies, users)
	}
}
