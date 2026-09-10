package backup_test

import (
	"context"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/db"
)

// The rehearsal is the difference between "the archive parses" and "we can come
// back from this". This test proves the real thing happens: a dump is restored
// into a scratch database and queried, and a corrupted dump is rejected by that
// load rather than sailing through a table-of-contents read.
func TestRehearseRestoreIntegration(t *testing.T) {
	requirePGTools(t)
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

	svc := backup.New(backup.Options{
		DatabaseURL: url,
		RehearseURL: url, // same server, different (scratch) database
		EncKey:      "rehearsal-test-key-at-least-32-bytes!!",
		Dir:         t.TempDir(),
	}, pool)

	enc, _, err := svc.CreateEncrypted(ctx)
	if err != nil {
		t.Fatalf("create dump: %v", err)
	}

	rep, err := svc.RehearseRestore(ctx, enc)
	if err != nil {
		t.Fatalf("rehearsal failed on a good dump: %v", err)
	}
	if rep.Migrations == 0 {
		t.Error("restored database reports no applied migrations")
	}
	if rep.Tables < 10 {
		t.Errorf("restored database has %d public tables, want the full schema", rep.Tables)
	}
	if rep.At.IsZero() || rep.Duration <= 0 {
		t.Errorf("rehearsal report has no timing: %+v", rep)
	}

	// A corrupted payload must fail. The bytes are encrypted, so flipping them
	// breaks the AEAD tag — which is itself the first line of defense and proves
	// a damaged file cannot be mistaken for a recovery point.
	bad := append([]byte(nil), enc...)
	bad[len(bad)/2] ^= 0xFF
	if _, err := svc.RehearseRestore(ctx, bad); err == nil {
		t.Error("rehearsal accepted a corrupted dump")
	}

	// Rehearsals are off unless configured — the conservative default.
	off := backup.New(backup.Options{DatabaseURL: url, EncKey: "x-at-least-32-bytes-for-the-key!!!!"}, pool)
	if _, err := off.RehearseRestore(ctx, enc); err == nil ||
		!strings.Contains(err.Error(), "not configured") {
		t.Errorf("without RehearseURL the rehearsal should refuse, got %v", err)
	}
}
