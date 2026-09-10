package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// Both operations here DERIVE what they write from the account's current balance.
// Reading that balance outside the writing transaction was a read-then-write race:
// concurrent requests each saw the same open amount and each posted it. Measured
// against the dev stack before the fix, 24 concurrent carry-forwards turned a €200
// balance into €10,400 of ledger postings (the surplus flips the balance negative,
// and a negative balance carries as a reverse posting that raises the source year
// again, so each round feeds the next), and 250 concurrent settles booked five
// payments instead of one.
//
// These tests pin the fix: whatever the concurrency, exactly one posting.
const raceConcurrency = 24

func raceSetup(t *testing.T) (context.Context, *sql.DB, *store.Store, int64, int64, int64, func()) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	// Unique per test AND per process: the suite shares one database, and year
	// numbers carry a unique constraint.
	base := 4800 + os.Getpid()%1000
	name := fmt.Sprintf("Race-Nachbar %s %d", t.Name(), os.Getpid())
	purgeFixtures(t, ctx, pool, fixtures{Years: []int{base, base + 1}, NeighborNames: []string{name}})

	baseID, err := st.CreateEmptyBase(ctx, base, "Race-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	fromYear, err := st.CreateBillingYear(ctx, base, baseID, "Jahr A")
	if err != nil {
		t.Fatalf("year A: %v", err)
	}
	toYear, err := st.CreateBillingYear(ctx, base+1, baseID, "Jahr B")
	if err != nil {
		t.Fatalf("year B: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, name, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	for _, y := range []int64{fromYear, toYear} {
		if err := st.AddNeighborToYear(ctx, y, nid); err != nil {
			t.Fatalf("add neighbor to %d: %v", y, err)
		}
	}
	// The open balance every request below will race for.
	if _, err := st.AddNeighborLedger(ctx, fromYear, nid, decimal.RequireFromString("200"), "Forderung", time.Now()); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	return ctx, pool, st, fromYear, toYear, nid, func() {
		purgeRootsByID(t, ctx, pool, []int64{fromYear, toYear}, []int64{nid}, []int64{baseID})
		pool.Close()
	}
}

// runConcurrently releases all calls from one barrier so they overlap rather than
// queue: without it the first would finish before the next began and the race
// could not appear even in unfixed code.
func runConcurrently(n int, fn func(int) error) []error {
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()
			errs[i] = fn(i)
		}(i)
	}
	start.Done()
	done.Wait()
	return errs
}

func TestCarryForwardRemainingConcurrentIntegration(t *testing.T) {
	ctx, _, st, fromYear, toYear, nid, cleanup := raceSetup(t)
	defer cleanup()

	errs := runConcurrently(raceConcurrency, func(int) error {
		_, err := st.CarryForwardRemaining(ctx, nid, fromYear, toYear, time.Now(), "Ins Folgejahr", "Übertrag")
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("carry %d: %v", i, err)
		}
	}

	// Source: the seeded 200 plus exactly one -200 transfer. Any duplicate shows up
	// both as extra rows and as a non-zero sum.
	from, err := st.ListNeighborLedger(ctx, fromYear, nid)
	if err != nil {
		t.Fatalf("ledger A: %v", err)
	}
	if len(from) != 2 {
		t.Errorf("year A has %d ledger rows after %d concurrent carries, want 2 (seed + one transfer)", len(from), raceConcurrency)
	}
	sumA, _ := st.NeighborLedgerSum(ctx, fromYear, nid)
	if !sumA.IsZero() {
		t.Errorf("year A balance = %s, want 0 — the balance was carried more than once", sumA)
	}
	to, err := st.ListNeighborLedger(ctx, toYear, nid)
	if err != nil {
		t.Fatalf("ledger B: %v", err)
	}
	if len(to) != 1 {
		t.Errorf("year B has %d ledger rows, want exactly 1", len(to))
	}
	sumB, _ := st.NeighborLedgerSum(ctx, toYear, nid)
	if !sumB.Equal(decimal.RequireFromString("200")) {
		t.Errorf("year B balance = %s, want 200", sumB)
	}
}

func TestSettleRemainingConcurrentIntegration(t *testing.T) {
	ctx, _, st, fromYear, _, nid, cleanup := raceSetup(t)
	defer cleanup()

	booked := make([]decimal.Decimal, raceConcurrency)
	errs := runConcurrently(raceConcurrency, func(i int) error {
		amt, err := st.SettleRemaining(ctx, fromYear, nid, time.Now(), "Restbetrag beglichen")
		booked[i] = amt
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("settle %d: %v", i, err)
		}
	}

	// Exactly one call may report having booked something; the rest must report
	// zero, which is what the handler turns into "Konto ist bereits ausgeglichen".
	nonZero := 0
	for _, a := range booked {
		if !a.IsZero() {
			nonZero++
		}
	}
	if nonZero != 1 {
		t.Errorf("%d of %d calls booked a payment, want exactly 1", nonZero, raceConcurrency)
	}
	pays, err := st.ListPayments(ctx, fromYear, nid)
	if err != nil {
		t.Fatalf("payments: %v", err)
	}
	if len(pays) != 1 {
		t.Errorf("%d payments recorded, want 1", len(pays))
	}
	sum, _ := st.NeighborPaymentSum(ctx, fromYear, nid)
	if !sum.Equal(decimal.RequireFromString("200")) {
		t.Errorf("payments total %s, want 200 — the balance was settled more than once", sum)
	}
}

// The two tests above exercise the whole path but cannot FORCE the race: read and
// write now sit in one short transaction, so even with the lock removed they
// almost always come out right. A concurrency test that cannot fail guards
// nothing, so the lock itself is asserted directly and deterministically here:
// hold the account row from outside, and the store call must wait for it.
//
// Remove the FOR UPDATE from lockAccount and these two fail immediately — the
// call sails past the held row and returns.
func assertWaitsForAccountLock(t *testing.T, ctx context.Context, pool *sql.DB, yearID, neighborID int64, call func(context.Context) error) {
	t.Helper()
	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin holder: %v", err)
	}
	defer holder.Rollback() //nolint:errcheck // test cleanup
	if _, err := holder.ExecContext(ctx,
		`SELECT 1 FROM billing_year_neighbors
		  WHERE billing_year_id=$1 AND neighbor_id=$2 FOR UPDATE`, yearID, neighborID); err != nil {
		t.Fatalf("hold account row: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = call(waitCtx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("returned successfully after %v while the account row was held elsewhere — it never took the lock", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed with %v, want the context deadline (i.e. it was waiting on the lock)", err)
	}
}

func TestCarryForwardRemainingWaitsForAccountLockIntegration(t *testing.T) {
	ctx, pool, st, fromYear, toYear, nid, cleanup := raceSetup(t)
	defer cleanup()
	assertWaitsForAccountLock(t, ctx, pool, fromYear, nid, func(c context.Context) error {
		_, err := st.CarryForwardRemaining(c, nid, fromYear, toYear, time.Now(), "Ins Folgejahr", "Übertrag")
		return err
	})
}

func TestSettleRemainingWaitsForAccountLockIntegration(t *testing.T) {
	ctx, pool, st, fromYear, _, nid, cleanup := raceSetup(t)
	defer cleanup()
	assertWaitsForAccountLock(t, ctx, pool, fromYear, nid, func(c context.Context) error {
		_, err := st.SettleRemaining(c, fromYear, nid, time.Now(), "Restbetrag beglichen")
		return err
	})
}

// PayoutCredit derives what it posts from the account balance exactly like
// SettleRemaining, so it needs the same lock. Before the fix the handler read
// the credit outside any transaction and posted it afterwards, and concurrent
// "Guthaben auszahlen" clicks each paid the same credit out.
func TestPayoutCreditConcurrentIntegration(t *testing.T) {
	ctx, _, st, yearID, _, nid, cleanup := raceSetup(t)
	defer cleanup()

	// raceSetup seeds +200; overshoot it so the account carries a credit of 300.
	if _, err := st.AddNeighborLedger(ctx, yearID, nid,
		decimal.RequireFromString("-500"), "Überzahlung", time.Now()); err != nil {
		t.Fatalf("seed credit: %v", err)
	}

	paid := make([]decimal.Decimal, raceConcurrency)
	errs := runConcurrently(raceConcurrency, func(i int) error {
		amount, err := st.PayoutCredit(ctx, yearID, nid, time.Now(), "Guthaben ausbezahlt")
		paid[i] = amount
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("payout %d: %v", i, err)
		}
	}

	// Exactly one call may report a payout; the rest must find nothing left.
	posted := 0
	total := decimal.Zero
	for _, p := range paid {
		if !p.IsZero() {
			posted++
			total = total.Add(p)
		}
	}
	if posted != 1 || !total.Equal(decimal.RequireFromString("300")) {
		t.Errorf("%d of %d concurrent payouts booked (total %s), want exactly 1 of 300",
			posted, raceConcurrency, total)
	}

	rows, err := st.ListNeighborLedger(ctx, yearID, nid)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("account has %d ledger rows after %d concurrent payouts, want 3 (two seeds + one payout)",
			len(rows), raceConcurrency)
	}
	sum, _ := st.NeighborLedgerSum(ctx, yearID, nid)
	if !sum.IsZero() {
		t.Errorf("balance = %s, want 0 — the credit was paid out more than once", sum)
	}
}
