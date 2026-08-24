package store_test

import (
	"context"
	"database/sql"
	"testing"
)

// fixtures names what one DB-backed test creates, so it can be removed again.
// Only the roots are listed: every child table cascades from billing_years,
// neighbors, price_bases and users, so deleting those four is enough.
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
// Order matters in one place only: billing_years.base_id is ON DELETE RESTRICT,
// so the years have to go before their price bases. Everything else cascades.
func purgeFixtures(t *testing.T, ctx context.Context, pool *sql.DB, f fixtures) {
	t.Helper()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("purge fixtures (%s): %v", query, err)
		}
	}
	if len(f.Years) > 0 {
		years := make([]int64, len(f.Years))
		for i, y := range f.Years {
			years[i] = int64(y)
		}
		exec(`DELETE FROM billing_years WHERE year = ANY($1)`, years)
		exec(`DELETE FROM price_bases WHERE year = ANY($1)`, years)
	}
	if len(f.NeighborNames) > 0 {
		exec(`DELETE FROM neighbors WHERE name = ANY($1)`, f.NeighborNames)
	}
	if f.UsernameLike != "" {
		exec(`DELETE FROM users WHERE username LIKE $1`, f.UsernameLike)
	}
}
