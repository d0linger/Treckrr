package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestDeleteBlockersIntegration pins the regression that guarding deletion on
// bookings alone allowed: neighbors and billing_years are referenced ON DELETE
// CASCADE by payments, neighbor_ledger and invoices too, so a neighbor with no
// bookings but a payment or a carry-forward used to pass the guard and take that
// financial history with it.
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

	pid := os.Getpid()
	yr := 4600 + pid%1000
	baseID, _ := st.CreateEmptyBase(ctx, yr, "DG-Basis")
	yearID, _ := st.CreateBillingYear(ctx, yr, baseID, "DG-Jahr")

	// A neighbor with nothing attached is deletable â€” the guard must not become
	// a blanket refusal.
	cleanID, err := st.CreateNeighbor(ctx, fmt.Sprintf("DG Sauber %d", pid), "")
	if err != nil {
		t.Fatalf("create clean neighbor: %v", err)
	}
	if b, err := st.NeighborDeleteBlockers(ctx, cleanID); err != nil {
		t.Fatalf("blockers(clean): %v", err)
	} else if b.Any() {
		t.Fatalf("clean neighbor reports blockers %+v, want none", b)
	}

	// A neighbor with a PAYMENT and no bookings: the case the old guard missed.
	payID, err := st.CreateNeighbor(ctx, fmt.Sprintf("DG Zahler %d", pid), "")
	if err != nil {
		t.Fatalf("create paying neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, payID); err != nil {
		t.Fatalf("add to year: %v", err)
	}
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

	// The year now inherits the same block through the same payment.
	yb, err := st.YearDeleteBlockers(ctx, yearID)
	if err != nil {
		t.Fatalf("blockers(year): %v", err)
	}
	if yb.Payments != 1 || !yb.Any() {
		t.Fatalf("year blockers = %+v, want Payments=1 and Any()=true", yb)
	}

	// A LEDGER position alone (the carry-forward case) also blocks.
	ledgerID, err := st.CreateNeighbor(ctx, fmt.Sprintf("DG Uebertrag %d", pid), "")
	if err != nil {
		t.Fatalf("create ledger neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, ledgerID); err != nil {
		t.Fatalf("add to year: %v", err)
	}
	if _, err := st.AddNeighborLedger(ctx, yearID, ledgerID,
		decimal.RequireFromString("-40.00"), "Ãœbertrag Vorjahr", time.Now()); err != nil {
		t.Fatalf("add ledger: %v", err)
	}
	if b, err := st.NeighborDeleteBlockers(ctx, ledgerID); err != nil {
		t.Fatalf("blockers(ledger): %v", err)
	} else if b.Ledger != 1 || b.Entries != 0 || !b.Any() {
		t.Fatalf("blockers = %+v, want Ledger=1, Entries=0, Any()=true", b)
	}

	// A soft-deleted payment must NOT block: the operator already removed it.
	softID, err := st.CreateNeighbor(ctx, fmt.Sprintf("DG Storniert %d", pid), "")
	if err != nil {
		t.Fatalf("create soft-delete neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, softID); err != nil {
		t.Fatalf("add to year: %v", err)
	}
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
	} else if b.Any() {
		t.Fatalf("soft-deleted payment still blocks: %+v", b)
	}
}
