package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/db"
	"treckrr/internal/models"
	"treckrr/internal/store"
)

// TestLedgerNetIntegration verifies that manual ledger postings net against the
// work bookings in both the per-neighbour sum and the paid/open split.
// Runs only when TEST_DATABASE_URL is set.
func TestLedgerNetIntegration(t *testing.T) {
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

	baseID, err := st.CreateEmptyBase(ctx, 2097, "Ledger-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2097, baseID, "Ledger-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Ledger-Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	// defer runs before pool.Close(); deleting the year cascades to entries/ledger.
	defer func() {
		_, _ = pool.ExecContext(ctx, `DELETE FROM billing_years WHERE id=$1`, yearID)
		_, _ = pool.ExecContext(ctx, `DELETE FROM price_bases WHERE id=$1`, baseID)
		_, _ = pool.ExecContext(ctx, `DELETE FROM neighbors WHERE id=$1`, nid)
	}()
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	// Work booking of 100.00.
	cost := decimal.RequireFromString("100")
	e := &models.Entry{NeighborID: nid, BillingYearID: yearID, Date: time.Now(),
		Hours: decimal.RequireFromString("1"), HourlyRate: cost, Cost: cost}
	if _, err := st.CreateEntry(ctx, e, nil); err != nil {
		t.Fatalf("entry: %v", err)
	}
	// Ledger: I owe 30 (credit) and an extra charge of 10 → net ledger -20.
	if _, err := st.AddNeighborLedger(ctx, yearID, nid, decimal.RequireFromString("-30"), "Gegenleistung"); err != nil {
		t.Fatalf("ledger credit: %v", err)
	}
	if _, err := st.AddNeighborLedger(ctx, yearID, nid, decimal.RequireFromString("10"), "Zuschlag"); err != nil {
		t.Fatalf("ledger charge: %v", err)
	}

	if sum, err := st.NeighborLedgerSum(ctx, yearID, nid); err != nil || !sum.Equal(decimal.RequireFromString("-20")) {
		t.Fatalf("NeighborLedgerSum = %s, %v; want -20", sum, err)
	}

	// Net owed = 100 - 20 = 80. Mark paid → paid total should be 80, open 0.
	if err := st.SetNeighborPaid(ctx, yearID, nid, true); err != nil {
		t.Fatalf("set paid: %v", err)
	}
	paid, open, err := st.YearPaymentTotals(ctx, yearID)
	if err != nil {
		t.Fatalf("YearPaymentTotals: %v", err)
	}
	if !paid.Equal(decimal.RequireFromString("80")) {
		t.Fatalf("paid = %s, want 80", paid)
	}
	if !open.Equal(decimal.Zero) {
		t.Fatalf("open = %s, want 0", open)
	}
}
