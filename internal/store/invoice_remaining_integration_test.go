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

// TestInvoiceRemainingIntegration proves the scan-to-pay amount: the remaining
// payable is the frozen gross less active credit notes and payments (not the full
// gross), so a partly-paid or credited invoice yields the correct QR amount. Runs
// only when TEST_DATABASE_URL is set.
func TestInvoiceRemainingIntegration(t *testing.T) {
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

	yr := 3500 + os.Getpid()%1000
	baseID, err := st.CreateEmptyBase(ctx, yr, "Rest-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Rest-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, fmt.Sprintf("Rest Nachbar %d", os.Getpid()), "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	// Issued invoice (gross 200) + an active credit note (gross -50). Distinct
	// numbers satisfy the (billing_year_id, number) unique constraint; different
	// kinds are allowed alongside the partial "one issued invoice" unique index.
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO invoices (billing_year_id, neighbor_id, number, gross, kind, status)
		 VALUES ($1,$2,$3,200,'invoice','issued')`, yearID, nid, fmt.Sprintf("%d-1", yr)); err != nil {
		t.Fatalf("insert invoice: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO invoices (billing_year_id, neighbor_id, number, gross, kind, status)
		 VALUES ($1,$2,$3,-50,'gutschrift','issued')`, yearID, nid, fmt.Sprintf("%d-2", yr)); err != nil {
		t.Fatalf("insert gutschrift: %v", err)
	}

	// Before any payment: 200 - 50 = 150.
	rest, err := st.InvoiceRemaining(ctx, yearID, nid)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if !rest.Equal(decimal.RequireFromString("150")) {
		t.Errorf("remaining (pre-payment) = %s, want 150", rest)
	}

	// A 30 payment reduces it to 120 — the QR must encode 120, not the 200 gross.
	if err := st.AddPayment(ctx, yearID, nid, decimal.RequireFromString("30"), time.Now(), ""); err != nil {
		t.Fatalf("add payment: %v", err)
	}
	rest, err = st.InvoiceRemaining(ctx, yearID, nid)
	if err != nil {
		t.Fatalf("remaining after payment: %v", err)
	}
	if !rest.Equal(decimal.RequireFromString("120")) {
		t.Errorf("remaining (after 30 payment) = %s, want 120", rest)
	}

	// A non-voided ledger posting of +40 raises the remaining to 160.
	if _, err := st.AddNeighborLedger(ctx, yearID, nid, decimal.RequireFromString("40"), "Verrechnung", time.Now()); err != nil {
		t.Fatalf("add ledger: %v", err)
	}
	// A VOIDED ledger posting of +1000 must be excluded by the `NOT voided` filter,
	// so the remaining stays 160.
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, voided)
		 VALUES ($1,$2,1000,true)`, yearID, nid); err != nil {
		t.Fatalf("insert voided ledger: %v", err)
	}
	rest, err = st.InvoiceRemaining(ctx, yearID, nid)
	if err != nil {
		t.Fatalf("remaining after ledger: %v", err)
	}
	if !rest.Equal(decimal.RequireFromString("160")) {
		t.Errorf("remaining (after +40 non-voided, +1000 voided ledger) = %s, want 160", rest)
	}
}
