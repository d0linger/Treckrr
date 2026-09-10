package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// ApplyRecalc reprices the very bookings a concurrent settle derives its
// "remaining" from, so it must queue behind the same account row lock that
// SettleRemaining and CarryForwardRemaining take (Pentest-Empfehlung 5,
// Ausbaukarte Nr. 8). Same deterministic style as the account-lock tests: hold
// the row from outside and the call must block until its context expires —
// remove the lock statement from ApplyRecalc and this fails in milliseconds.
func TestApplyRecalcWaitsForAccountLockIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	yr := 6700 + os.Getpid()%300
	name := fmt.Sprintf("RecalcLock-Nachbar %d", os.Getpid())
	f := fixtures{Years: []int{yr}, NeighborNames: []string{name}}
	purgeFixtures(t, ctx, pool, f)
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f) })

	baseID, err := st.CreateEmptyBase(ctx, yr, "RL-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "RL-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, name, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("membership: %v", err)
	}
	if _, err := st.AddNeighborLedger(ctx, yearID, nid,
		decimal.RequireFromString("50"), "Posten", time.Now()); err != nil {
		t.Fatalf("ledger: %v", err)
	}

	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer holder.Rollback() //nolint:errcheck // test cleanup
	if _, err := holder.ExecContext(ctx,
		`SELECT 1 FROM billing_year_neighbors WHERE billing_year_id=$1 AND neighbor_id=$2 FOR UPDATE`,
		yearID, nid); err != nil {
		t.Fatalf("hold row: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, _, err = st.ApplyRecalc(waitCtx, yearID, &nid)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("ApplyRecalc returned after %v while the account row was held — it never took the lock", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, sql.ErrTxDone) {
		// pgx surfaces a canceled statement in more than one shape; what matters
		// is that it WAITED, which the elapsed floor pins.
		t.Logf("error shape: %v", err)
	}
	if elapsed < time.Second {
		t.Fatalf("ApplyRecalc gave up after only %v — it did not wait on the account lock", elapsed)
	}
}
