package store_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// scratchStore isolates tests that operate on singleton settings or global
// queues. Random lowercase hex makes the SQL identifier safe and collision
// resistant even when multiple containers reuse the same process ID.
func scratchStore(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	base, err := neturl.Parse(dsn)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	adminURL := *base
	adminURL.Path = "/postgres"
	admin, err := sql.Open("pgx", adminURL.String())
	if err != nil {
		t.Fatalf("open test database admin: %v", err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate scratch database name: %v", err)
	}
	name := "treckrr_test_" + hex.EncodeToString(suffix[:])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := admin.ExecContext(cleanupCtx, `DROP DATABASE `+name+` WITH (FORCE)`); err != nil {
			t.Errorf("drop owned scratch database %s: %v", name, err)
		}
	})
	scratchURL := *base
	scratchURL.Path = "/" + name
	pool, err := db.Connect(ctx, scratchURL.String())
	if err != nil {
		t.Fatalf("connect scratch database: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate scratch database: %v", err)
	}
	return store.New(pool, "test-encryption-secret"), pool
}

func scratchBookingFixture(t *testing.T) (*store.Store, *sql.DB, int64, int64) {
	t.Helper()
	st, pool := scratchStore(t)
	ctx := context.Background()
	baseID, err := st.CreateEmptyBase(ctx, 2026, "Review fixture")
	if err != nil {
		t.Fatal(err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2026, baseID, "Review fixture")
	if err != nil {
		t.Fatal(err)
	}
	neighborID, err := st.CreateNeighbor(ctx, "Review neighbor", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, neighborID); err != nil {
		t.Fatal(err)
	}
	return st, pool, yearID, neighborID
}

func waitForDatabaseBlock(t *testing.T, ctx context.Context, pool *sql.DB, blockerPID int) {
	t.Helper()
	for {
		var waiting bool
		if err := pool.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)))`, blockerPID).Scan(&waiting); err != nil {
			t.Fatalf("wait for blocked query: %v", err)
		}
		if waiting {
			return
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("query did not block: %v", ctx.Err())
		}
	}
}

// fixtures names what one DB-backed test creates, so it can be removed again.
// Only the roots are listed; purgeFixtures knows which children to clear first.
type fixtures struct {
	// Years matches both billing_years.year and price_bases.year. Tests use the
	// 2085-2099 range, which real data never reaches.
	Years []int
	// NeighborNames are removed by exact name (neighbors.name is UNIQUE, which is
	// what a rerun collides on).
	NeighborNames []string
	// UsernameLike is a LIKE pattern for the users a test creates, e.g. "sh04\_%".
	UsernameLike string
}

// purgeFixtures removes a test's fixtures. Call it BEFORE seeding as well as
// after: the integration suite shares one database, and several of its tests seed
// rows under names that are UNIQUE (neighbors.name, billing_years.year,
// users.username). Without this, a suite that is green against a fresh database
// fails on its SECOND run — which is exactly what happened, invisibly, because CI
// starts a new Postgres service container every time. Purging up front also means
// a crashed run does not leave the database permanently poisoned.
//
// Order matters. billing_years.base_id has always been ON DELETE RESTRICT, so the
// years go before their price bases. Since 0039 the money- and tax-bearing child
// tables (entries, neighbor_ledger, payments, invoices, beleg_sends) are RESTRICT
// too — that is the point of the migration — so this helper can no longer lean on
// a cascade to sweep them and deletes them explicitly first. The remaining
// children (billing_year_neighbors, beleg_shares, recurring_entries) still
// cascade; they are cleared here anyway so the order is stated rather than
// inferred.
func purgeFixtures(t *testing.T, ctx context.Context, pool *sql.DB, f fixtures) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("purge fixtures (%s): %v", query, err)
		}
	}
	// Children that no longer cascade, in dependency order. Each is scoped by a
	// subquery on the parent so only this test's rows are touched.
	purgeChildren := func(fkColumn, parentSelect string, args ...any) {
		t.Helper()
		for _, child := range []string{
			"beleg_sends", "invoices", "payments", "neighbor_ledger", "entries",
			"beleg_shares", "billing_year_neighbors",
		} {
			// Every table listed carries BOTH billing_year_id and neighbor_id, so the
			// same loop serves either parent.
			exec(`DELETE FROM `+child+` WHERE `+fkColumn+` IN (`+parentSelect+`)`, args...)
		}
	}

	if len(f.Years) > 0 {
		years := make([]int64, len(f.Years))
		for i, y := range f.Years {
			years[i] = int64(y)
		}
		purgeChildren("billing_year_id", `SELECT id FROM billing_years WHERE year = ANY($1)`, years)
		exec(`DELETE FROM billing_years WHERE year = ANY($1)`, years)
		exec(`DELETE FROM price_bases WHERE year = ANY($1)`, years)
	}
	if len(f.NeighborNames) > 0 {
		purgeChildren("neighbor_id", `SELECT id FROM neighbors WHERE name = ANY($1)`, f.NeighborNames)
		exec(`DELETE FROM recurring_entries WHERE neighbor_id IN (SELECT id FROM neighbors WHERE name = ANY($1))`, f.NeighborNames)
		exec(`DELETE FROM neighbors WHERE name = ANY($1)`, f.NeighborNames)
	}
	if f.UsernameLike != "" {
		// User offboarding is now deliberately logical so audit attribution remains
		// intact. Test fixtures still need physical cleanup because their stable
		// usernames must be reusable across runs. Remove only the matching test
		// actors and their audit rows through the same transaction-local retention
		// opt-in used by PurgeAuditLog; otherwise ON DELETE SET NULL would attempt a
		// forbidden UPDATE of the append-only audit table.
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("purge user fixtures: begin: %v", err)
		}
		defer tx.Rollback() //nolint:errcheck // no-op after Commit
		if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_prune = 'on'`); err != nil {
			t.Fatalf("purge user fixtures: audit opt-in: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM audit_log
			WHERE user_id IN (SELECT id FROM users WHERE username LIKE $1)`, f.UsernameLike); err != nil {
			t.Fatalf("purge user fixtures: audit rows: %v", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE username LIKE $1`, f.UsernameLike); err != nil {
			t.Fatalf("purge user fixtures: users: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("purge user fixtures: commit: %v", err)
		}
	}
}

// purgeRootsByID is purgeFixtures for tests that track the ids they created
// rather than fixed names/years. Since 0039 the money- and tax-bearing child
// tables are ON DELETE RESTRICT, so deleting a root no longer sweeps them and
// they have to go first — several tests previously did `DELETE FROM
// billing_years WHERE id=$1` with a "cascades to entries" comment, which now
// fails and leaves the fixture behind for the next run to collide with.
//
// Pass nil for anything a test did not create. Errors are reported, not ignored:
// a silent cleanup failure is what let those leftovers accumulate unnoticed.
func purgeRootsByID(t *testing.T, ctx context.Context, pool *sql.DB, yearIDs, neighborIDs, baseIDs []int64) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, query, args...); err != nil {
			t.Errorf("purge roots (%s): %v", query, err)
		}
	}
	children := []string{
		"beleg_sends", "invoices", "payments", "neighbor_ledger", "entries",
		"beleg_shares", "billing_year_neighbors",
	}
	if len(yearIDs) > 0 {
		for _, c := range children {
			exec(`DELETE FROM `+c+` WHERE billing_year_id = ANY($1)`, yearIDs)
		}
	}
	if len(neighborIDs) > 0 {
		for _, c := range children {
			exec(`DELETE FROM `+c+` WHERE neighbor_id = ANY($1)`, neighborIDs)
		}
		exec(`DELETE FROM recurring_entries WHERE neighbor_id = ANY($1)`, neighborIDs)
	}
	// Roots last, and years before their price bases (billing_years.base_id has
	// always been RESTRICT).
	if len(yearIDs) > 0 {
		exec(`DELETE FROM billing_years WHERE id = ANY($1)`, yearIDs)
	}
	if len(baseIDs) > 0 {
		exec(`DELETE FROM price_bases WHERE id = ANY($1)`, baseIDs)
	}
	if len(neighborIDs) > 0 {
		exec(`DELETE FROM neighbors WHERE id = ANY($1)`, neighborIDs)
	}
}
