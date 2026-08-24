package store_test

import (
	"context"
	"database/sql"
	"testing"
)

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
		exec(`DELETE FROM users WHERE username LIKE $1`, f.UsernameLike)
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
