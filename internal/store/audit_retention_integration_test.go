package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestAuditRetentionStaggeredIntegration verifies the staggered retention:
// a short-lived security event (login) past the short cutoff is purged, while
// a business/tax-relevant event (an entry "update") of the same age survives
// until the long cutoff. Runs only when TEST_DATABASE_URL is set.
func TestAuditRetentionStaggeredIntegration(t *testing.T) {
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

	// Unique markers so this test is independent of any other rows and cleans up
	// after itself even if it fails midway.
	const shortMarker = "ret_short_marker"
	const longMarker = "ret_business_marker"
	// audit_log is append-only (0036 guard): a bare DELETE is blocked, so teardown
	// deletes through the same treckrr.allow_audit_prune opt-in the purge uses.
	clear := func() {
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			t.Errorf("clear begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_prune = 'on'`); err != nil {
			t.Errorf("clear set flag: %v", err)
			return
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM audit_log WHERE detail IN ($1,$2)`, shortMarker, longMarker); err != nil {
			t.Errorf("clear test audit rows: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("clear commit: %v", err)
		}
	}
	clear()
	defer clear()

	// Insert both rows already backdated to 2 years ago — older than the short window
	// (1y) but younger than the long window (7y). We INSERT directly with an explicit
	// created_at (rather than AddAudit + UPDATE) because the append-only guard blocks
	// UPDATE unconditionally. The business row uses the real action a live entry edit
	// emits ("update" / entity "entry"), so this test fails if someone ever moves
	// "update" onto the short list — not a synthetic action string that never regresses.
	old := time.Now().AddDate(-2, 0, 0)
	for _, row := range []struct{ action, entity, detail string }{
		{"login", "auth", shortMarker},
		{"update", "entry", longMarker},
	} {
		if _, err := pool.ExecContext(ctx, `
			INSERT INTO audit_log (username, action, entity, entity_id, detail, ip, created_at)
			VALUES ('tester', $1, $2, '', $3, '10.0.0.1', $4)`,
			row.action, row.entity, row.detail, old); err != nil {
			t.Fatalf("insert backdated %s row: %v", row.detail, err)
		}
	}

	count := func(detail string) int {
		var n int
		if err := pool.QueryRowContext(ctx,
			`SELECT count(*) FROM audit_log WHERE detail=$1`, detail).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", detail, err)
		}
		return n
	}

	// First purge: short cutoff = now-1y, long cutoff = now-7y. The 2-year-old
	// login row is past the short cutoff and must go; the business row is younger
	// than 7y and must stay.
	now := time.Now()
	if _, err := st.PurgeAuditLog(ctx, now.AddDate(-1, 0, 0), now.AddDate(-7, 0, 0)); err != nil {
		t.Fatalf("purge #1: %v", err)
	}
	if got := count(shortMarker); got != 0 {
		t.Fatalf("short-lived login row not purged: count=%d, want 0", got)
	}
	if got := count(longMarker); got != 1 {
		t.Fatalf("business row wrongly purged at short cutoff: count=%d, want 1", got)
	}

	// Second purge with the long cutoff moved past the row's age: now the business
	// row is also eligible and must go.
	if _, err := st.PurgeAuditLog(ctx, now.AddDate(-1, 0, 0), now.AddDate(-1, 0, 0)); err != nil {
		t.Fatalf("purge #2: %v", err)
	}
	if got := count(longMarker); got != 0 {
		t.Fatalf("business row not purged past long cutoff: count=%d, want 0", got)
	}
}
