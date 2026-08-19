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
// a business/tax-relevant event (entry_update) of the same age survives until
// the long cutoff. Runs only when TEST_DATABASE_URL is set.
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
	clear := func() {
		if _, err := pool.ExecContext(ctx,
			`DELETE FROM audit_log WHERE detail IN ($1,$2)`, shortMarker, longMarker); err != nil {
			t.Errorf("clear test audit rows: %v", err)
		}
	}
	clear()
	defer clear()

	// One short-lived (login) row and one business (entry_update) row.
	if err := st.AddAudit(ctx, nil, "tester", "login", "auth", "", shortMarker, "10.0.0.1"); err != nil {
		t.Fatalf("add login row: %v", err)
	}
	if err := st.AddAudit(ctx, nil, "tester", "entry_update", "entry", "1", longMarker, "10.0.0.1"); err != nil {
		t.Fatalf("add business row: %v", err)
	}

	// Backdate both rows to 2 years ago — older than the short window (1y) but
	// younger than the long window (7y).
	old := time.Now().AddDate(-2, 0, 0)
	if _, err := pool.ExecContext(ctx,
		`UPDATE audit_log SET created_at=$1 WHERE detail IN ($2,$3)`, old, shortMarker, longMarker); err != nil {
		t.Fatalf("backdate rows: %v", err)
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
