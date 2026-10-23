//go:build integration

package db_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/db"
)

// TestNonSuperuserDatabaseRoleIntegration exercises migrations, trusted search
// extensions and encrypted backup/restore under a database owner that cannot
// create roles, databases or server-side programs. It uses only a scratch DB.
func TestNonSuperuserDatabaseRoleIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	for _, tool := range []string{"pg_dump", "pg_restore"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not available", tool)
		}
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Host == "" {
		t.Fatal("TEST_DATABASE_URL must be a PostgreSQL URL")
	}
	admin, err := db.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	if super, err := db.IsSuperuser(t.Context(), admin); err != nil {
		t.Fatal(err)
	} else if !super {
		t.Skip("scratch role creation requires a superuser test connection")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	name := "treckrr_role_test_" + hex.EncodeToString(nonce[:])
	quoted := pgx.Identifier{name}.Sanitize()
	const password = "disposable-role-test-password"
	if _, err := admin.ExecContext(t.Context(), `CREATE ROLE `+quoted+` LOGIN
		NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '`+password+`'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP ROLE `+quoted); err != nil {
			t.Errorf("cleanup scratch role: %v", err)
		}
	})
	if _, err := admin.ExecContext(t.Context(), `CREATE DATABASE `+quoted+` OWNER `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, `DROP DATABASE `+quoted+` WITH (FORCE)`); err != nil {
			t.Errorf("cleanup scratch database: %v", err)
		}
	})
	u.Path = "/" + name
	u.User = url.UserPassword(name, password)
	q := u.Query()
	for _, key := range []string{"dbname", "user", "password"} {
		q.Del(key)
	}
	u.RawQuery = q.Encode()
	pool, err := db.Connect(t.Context(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if super, err := db.IsSuperuser(t.Context(), pool); err != nil || super {
		t.Fatalf("application superuser=%v error=%v", super, err)
	}
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var trusted int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM pg_extension
		WHERE extname IN ('pg_trgm','unaccent') AND extowner = current_user::regrole`).Scan(&trusted); err != nil || trusted != 2 {
		t.Fatalf("owned search extensions=%d error=%v", trusted, err)
	}
	if _, err := pool.ExecContext(t.Context(), `COPY (SELECT 1) TO PROGRAM 'true'`); err == nil {
		t.Fatal("application role can execute server-side programs")
	}
	if _, err := pool.ExecContext(t.Context(), `INSERT INTO neighbors(name) VALUES ('Before restore')`); err != nil {
		t.Fatal(err)
	}
	const key = "synthetic-role-backup-test-key"
	svc := backup.New(backup.Options{DatabaseURL: u.String(), EncKey: key, Dir: t.TempDir()}, pool)
	enc, _, err := svc.CreateEncrypted(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := backup.DecryptWith(enc, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(t.Context(), `UPDATE neighbors SET name = 'After backup'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreRaw(t.Context(), raw, u.String()); err != nil {
		t.Fatal(err)
	}
	db.ResetPool(pool)
	if err := db.Migrate(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err := pool.QueryRowContext(t.Context(), `SELECT name FROM neighbors`).Scan(&restored); err != nil || restored != "Before restore" {
		t.Fatalf("restored name=%q error=%v", restored, err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.IsSuperuser(t.Context(), pool); err == nil {
		t.Fatal("role query failure was silently accepted")
	}
}
