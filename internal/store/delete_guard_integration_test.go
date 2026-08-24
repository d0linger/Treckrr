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

// TestDeleteBlockersIntegration pins the regression that guarding deletion on
// bookings alone allowed: neighbors and billing_years are referenced by payments,
// neighbor_ledger and invoices too, so a neighbor with no bookings but a payment
// or a carry-forward used to pass the guard and — under the old ON DELETE CASCADE
// — take that financial history with it.
//
// It checks BOTH layers: the precheck that produces a helpful message, and the
// 0039 RESTRICT constraints that are the actual integrity boundary. The precheck
// alone is a SELECT followed by a separate DELETE and cannot be trusted to hold
// under concurrency.
func TestDeleteBlockersIntegration(t *testing.T) {
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

	// Fixed names in the reserved range, purged before and after, per the suite's
	// convention — a pid-derived year silently collides with another test's.
	f := fixtures{
		Years:         []int{2089},
		NeighborNames: []string{"DG-Sauber", "DG-Zahler", "DG-Uebertrag", "DG-Storniert"},
	}
	purgeFixtures(t, ctx, pool, f)
	defer purgeFixtures(t, ctx, pool, f)

	baseID, err := st.CreateEmptyBase(ctx, 2089, "DG-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2089, baseID, "DG-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	mkNeighbor := func(name string) int64 {
		t.Helper()
		id, err := st.CreateNeighbor(ctx, name, "")
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, id); err != nil {
			t.Fatalf("add %s to year: %v", name, err)
		}
		return id
	}

	// A neighbor with nothing attached stays deletable — the guard must not become
	// a blanket refusal, and membership alone still cascades.
	cleanID := mkNeighbor("DG-Sauber")
	if b, err := st.NeighborDeleteBlockers(ctx, cleanID); err != nil {
		t.Fatalf("blockers(clean): %v", err)
	} else if b.Any() {
		t.Fatalf("clean neighbor reports blockers %+v, want none", b)
	}
	if err := st.DeleteNeighbor(ctx, cleanID); err != nil {
		t.Fatalf("deleting an unencumbered neighbor must still work, got: %v", err)
	}

	// A neighbor with a PAYMENT and no bookings: the case the old guard missed.
	payID := mkNeighbor("DG-Zahler")
	if err := st.AddPayment(ctx, yearID, payID, decimal.RequireFromString("92.00"), time.Now(), ""); err != nil {
		t.Fatalf("add payment: %v", err)
	}
	b, err := st.NeighborDeleteBlockers(ctx, payID)
	if err != nil {
		t.Fatalf("blockers(payment): %v", err)
	}
	if b.Entries != 0 {
		t.Fatalf("Entries = %d, want 0 (this neighbor has no bookings)", b.Entries)
	}
	if b.Payments != 1 || !b.Any() {
		t.Fatalf("blockers = %+v, want Payments=1 and Any()=true", b)
	}

	// The database must refuse it too — this is what survives the race the
	// precheck cannot close.
	if err := st.DeleteNeighbor(ctx, payID); !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("DeleteNeighbor with retained payment = %v, want ErrHasHistory", err)
	}
	var payments int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM payments WHERE neighbor_id=$1`, payID).Scan(&payments); err != nil {
		t.Fatalf("count payments: %v", err)
	}
	if payments != 1 {
		t.Fatalf("payment history changed after a refused delete: %d rows, want 1", payments)
	}

	// The year inherits the same protection through the same payment.
	yb, err := st.YearDeleteBlockers(ctx, yearID)
	if err != nil {
		t.Fatalf("blockers(year): %v", err)
	}
	if yb.Payments != 1 || !yb.Any() {
		t.Fatalf("year blockers = %+v, want Payments=1 and Any()=true", yb)
	}
	if err := st.DeleteBillingYear(ctx, yearID); !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("DeleteBillingYear with retained payment = %v, want ErrHasHistory", err)
	}

	// A LEDGER position alone (the carry-forward case) also blocks.
	ledgerID := mkNeighbor("DG-Uebertrag")
	if _, err := st.AddNeighborLedger(ctx, yearID, ledgerID,
		decimal.RequireFromString("-40.00"), "Uebertrag Vorjahr", time.Now()); err != nil {
		t.Fatalf("add ledger: %v", err)
	}
	if b, err := st.NeighborDeleteBlockers(ctx, ledgerID); err != nil {
		t.Fatalf("blockers(ledger): %v", err)
	} else if b.Ledger != 1 || b.Entries != 0 || !b.Any() {
		t.Fatalf("blockers = %+v, want Ledger=1, Entries=0, Any()=true", b)
	}
	if err := st.DeleteNeighbor(ctx, ledgerID); !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("DeleteNeighbor with retained ledger = %v, want ErrHasHistory", err)
	}

	// A soft-deleted payment STILL blocks: the row exists, so RESTRICT refuses the
	// delete because of it. The precheck has to agree, or the refusal arrives with
	// no explanation. It is self-healing — PurgeDeletedPayments clears it later.
	softID := mkNeighbor("DG-Storniert")
	if err := st.AddPayment(ctx, yearID, softID, decimal.RequireFromString("10"), time.Now(), ""); err != nil {
		t.Fatalf("add payment: %v", err)
	}
	ps, err := st.ListPayments(ctx, yearID, softID)
	if err != nil || len(ps) == 0 {
		t.Fatalf("list payments: %v (len %d)", err, len(ps))
	}
	if _, err := st.DeletePayment(ctx, ps[0].ID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if b, err := st.NeighborDeleteBlockers(ctx, softID); err != nil {
		t.Fatalf("blockers(soft-deleted): %v", err)
	} else if b.Payments != 1 {
		t.Fatalf("soft-deleted payment reports Payments=%d, want 1 — the precheck "+
			"must agree with the constraint, which still sees the row", b.Payments)
	}
	if err := st.DeleteNeighbor(ctx, softID); !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("DeleteNeighbor with soft-deleted payment = %v, want ErrHasHistory", err)
	}
}
