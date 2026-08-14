package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestDunningRowsIntegration proves the overdue detection: a neighbor with an
// issued invoice whose open amount is positive and whose due date (issue + term)
// has passed appears once; a fully-paid or not-yet-due invoice does not. Runs
// only when TEST_DATABASE_URL is set.
func TestDunningRowsIntegration(t *testing.T) {
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

	yr := 3000 + os.Getpid()%1000 // keep unique-ish across runs on the shared DB
	baseID, err := st.CreateEmptyBase(ctx, yr, "Dunning-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Dunning-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, fmt.Sprintf("Dunning Nachbar %d", os.Getpid()), "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Arbeit",
		Unit: "h", Hours: decimal.RequireFromString("1"), HourlyRate: decimal.RequireFromString("100"),
		Cost: decimal.RequireFromString("100.00"),
	}, nil); err != nil {
		t.Fatalf("entry: %v", err)
	}
	// Issue an invoice dated 30 days ago with a frozen GROSS of 113 (net 100 + 13%
	// VAT), while the booking net is 100. DunningRows must dun the gross (113), not
	// the net (100) — the net/gross bug this test guards against.
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO invoices (billing_year_id, neighbor_id, number, issued_on, gross)
		 VALUES ($1,$2,$3, CURRENT_DATE - 30, 113)`, yearID, nid, fmt.Sprintf("%d-999", yr)); err != nil {
		t.Fatalf("insert invoice: %v", err)
	}

	now := time.Now()

	// With a 14-day term and a 30-day-old invoice, the gross 113.00 is overdue.
	rows, err := st.DunningRows(ctx, yearID, 14, now)
	if err != nil {
		t.Fatalf("dunning: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.NeighborID == nid {
			found = true
			if !r.Open.Equal(decimal.RequireFromString("113.00")) {
				t.Errorf("open = %s, want 113.00 (invoice gross, not net 100)", r.Open)
			}
			if r.DaysOverdue < 15 { // 30 days old − 14 day term ≈ 16
				t.Errorf("days overdue = %d, want >= 15", r.DaysOverdue)
			}
		}
	}
	if !found {
		t.Fatalf("overdue neighbor not returned")
	}

	// A generous 60-day term makes it not yet due → not listed.
	rows, err = st.DunningRows(ctx, yearID, 60, now)
	if err != nil {
		t.Fatalf("dunning (long term): %v", err)
	}
	for _, r := range rows {
		if r.NeighborID == nid {
			t.Errorf("neighbor should not be overdue with a 60-day term")
		}
	}

	// Record a payment covering the full gross (113) → no longer open → not listed.
	if err := st.AddPayment(ctx, yearID, nid, decimal.RequireFromString("113.00"), time.Now(), ""); err != nil {
		t.Fatalf("add payment: %v", err)
	}
	rows, err = st.DunningRows(ctx, yearID, 14, now)
	if err != nil {
		t.Fatalf("dunning (paid): %v", err)
	}
	for _, r := range rows {
		if r.NeighborID == nid {
			t.Errorf("fully-paid neighbor should not appear in the dunning list")
		}
	}
}
