package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestCarryForwardCascadeIntegration verifies the carry-forward integrity fix:
// the two ledger sides share a transfer_id, and removing one side reverses BOTH,
// so the open balance reopens in the source year instead of vanishing.
// Runs only when TEST_DATABASE_URL is set.
func TestCarryForwardCascadeIntegration(t *testing.T) {
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

	// Purge by the STATIC values first: purgeRootsByID below needs ids that do not
	// exist yet, so a run that crashed before its deferred cleanup would otherwise
	// collide with its own leftovers on the unique year/name.
	purgeFixtures(t, ctx, pool, fixtures{Years: []int{2100, 2101}, NeighborNames: []string{"Carry-Nachbar"}})

	baseID, err := st.CreateEmptyBase(ctx, 2100, "Carry-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	fromYear, err := st.CreateBillingYear(ctx, 2100, baseID, "Jahr A")
	if err != nil {
		t.Fatalf("year A: %v", err)
	}
	toYear, err := st.CreateBillingYear(ctx, 2101, baseID, "Jahr B")
	if err != nil {
		t.Fatalf("year B: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Carry-Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	defer purgeRootsByID(t, ctx, pool, []int64{fromYear, toYear}, []int64{nid}, []int64{baseID})
	for _, y := range []int64{fromYear, toYear} {
		if err := st.AddNeighborToYear(ctx, y, nid); err != nil {
			t.Fatalf("add neighbor to %d: %v", y, err)
		}
	}

	remaining := func(year int64) decimal.Decimal {
		l, _ := st.NeighborLedgerSum(ctx, year, nid)
		p, _ := st.NeighborPaymentSum(ctx, year, nid)
		return l.Sub(p) // no bookings in this test, so net == ledger
	}
	hundred := decimal.RequireFromString("100")

	// Open balance of 100 in year A.
	if _, err := st.AddNeighborLedger(ctx, fromYear, nid, hundred, "Forderung", time.Now()); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if !remaining(fromYear).Equal(hundred) {
		t.Fatalf("pre-carry remaining A = %s, want 100", remaining(fromYear))
	}

	// Carry the open 100 into year B: A settles to 0, B opens at 100.
	if err := st.CarryForward(ctx, nid, fromYear, toYear, hundred, time.Now(), "Ins Folgejahr", "Übertrag"); err != nil {
		t.Fatalf("carry: %v", err)
	}
	if !remaining(fromYear).IsZero() {
		t.Fatalf("after carry remaining A = %s, want 0", remaining(fromYear))
	}
	if !remaining(toYear).Equal(hundred) {
		t.Fatalf("after carry remaining B = %s, want 100", remaining(toYear))
	}

	// Remove year B's side via its transfer_id — both sides must go.
	ledB, err := st.ListNeighborLedger(ctx, toYear, nid)
	if err != nil || len(ledB) != 1 {
		t.Fatalf("ledger B: %v (n=%d)", err, len(ledB))
	}
	if ledB[0].TransferID == "" {
		t.Fatalf("carry posting has no transfer_id")
	}
	// Transfer reversals are ledger edits, not settlement actions. A completed
	// participating year must be reopened before either side can be changed.
	if err := st.SetYearStatus(ctx, fromYear, "completed"); err != nil {
		t.Fatalf("complete source year: %v", err)
	}
	if err := st.SetLedgerVoidedTransfer(ctx, ledB[0].TransferID, true, "closed-year attempt"); !errors.Is(err, store.ErrYearCompleted) {
		t.Fatalf("void transfer in completed year: got %v, want ErrYearCompleted", err)
	}
	if err := st.DeleteLedgerTransfer(ctx, ledB[0].TransferID); !errors.Is(err, store.ErrYearCompleted) {
		t.Fatalf("delete transfer in completed year: got %v, want ErrYearCompleted", err)
	}
	if err := st.SetYearStatus(ctx, fromYear, "in_progress"); err != nil {
		t.Fatalf("reopen source year: %v", err)
	}
	if err := st.DeleteLedgerTransfer(ctx, ledB[0].TransferID); err != nil {
		t.Fatalf("delete transfer: %v", err)
	}
	if !remaining(fromYear).Equal(hundred) {
		t.Fatalf("after undo remaining A = %s, want 100 (reopened)", remaining(fromYear))
	}
	if !remaining(toYear).IsZero() {
		t.Fatalf("after undo remaining B = %s, want 0", remaining(toYear))
	}
}
