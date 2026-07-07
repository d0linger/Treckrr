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

// TestMoneyRoundTripIntegration proves exact decimals survive the NUMERIC
// columns via shopspring's Scanner/Valuer (no float drift). Runs only when
// TEST_DATABASE_URL is set.
func TestMoneyRoundTripIntegration(t *testing.T) {
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

	baseID, err := st.CreateEmptyBase(ctx, 2099, "Test-Basis")
	if err != nil {
		t.Fatalf("create base: %v", err)
	}

	cost := decimal.RequireFromString("0.335")
	if _, err := st.CreateLoadLevel(ctx, baseID, "prüf", cost, 1); err != nil {
		t.Fatalf("create load level: %v", err)
	}
	levels, err := st.ListLoadLevels(ctx, baseID)
	if err != nil || len(levels) == 0 {
		t.Fatalf("list load levels: %v (n=%d)", err, len(levels))
	}
	if !levels[0].CostPerPS.Equal(cost) {
		t.Fatalf("cost_per_ps round-trip = %s, want %s", levels[0].CostPerPS, cost)
	}

	width := decimal.RequireFromString("3.06")
	ab := decimal.RequireFromString("12")
	if _, err := st.CreateMachine(ctx, baseID, "Mäher", width, ab, "", 0); err != nil {
		t.Fatalf("create machine: %v", err)
	}
	machines, err := st.ListMachines(ctx, baseID)
	if err != nil || len(machines) == 0 {
		t.Fatalf("list machines: %v (n=%d)", err, len(machines))
	}
	if !machines[0].WorkingWidth.Equal(width) || !machines[0].CostPerAB.Equal(ab) {
		t.Fatalf("machine round-trip width=%s cost=%s, want %s/%s",
			machines[0].WorkingWidth, machines[0].CostPerAB, width, ab)
	}
	// Machine hourly rate = 3.06 * 12 = 36.72 exactly.
	if got := machines[0].HourlyRate(); got.StringFixed(2) != "36.72" {
		t.Fatalf("HourlyRate = %s, want 36.72", got.StringFixed(2))
	}
}

// TestYearPaymentTotalsIntegration verifies the single-query paid/open split
// used by the stats pages matches the intended per-neighbour aggregation, with
// exact decimals. Runs only when TEST_DATABASE_URL is set.
func TestYearPaymentTotalsIntegration(t *testing.T) {
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

	baseID, err := st.CreateEmptyBase(ctx, 2098, "Zahlungs-Basis")
	if err != nil {
		t.Fatalf("create base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2098, baseID, "Zahlungsjahr")
	if err != nil {
		t.Fatalf("create year: %v", err)
	}

	cases := []struct {
		name string
		cost string
		paid bool
	}{
		{"Bezahlt-A", "100.50", true},
		{"Bezahlt-B", "49.50", true},
		{"Offen-A", "200.25", false},
		{"Offen-leer", "0", false}, // member with no entries must not skew totals
	}
	var wantPaid, wantOpen decimal.Decimal
	for _, c := range cases {
		nid, err := st.CreateNeighbor(ctx, c.name, "")
		if err != nil {
			t.Fatalf("create neighbor: %v", err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
			t.Fatalf("add neighbor: %v", err)
		}
		cost := decimal.RequireFromString(c.cost)
		if cost.IsPositive() {
			e := &models.Entry{
				NeighborID: nid, BillingYearID: yearID, Date: time.Now(),
				Hours: decimal.RequireFromString("1"), HourlyRate: cost, Cost: cost,
			}
			if _, err := st.CreateEntry(ctx, e, nil); err != nil {
				t.Fatalf("create entry: %v", err)
			}
		}
		if c.paid {
			if err := st.SetNeighborPaid(ctx, yearID, nid, true); err != nil {
				t.Fatalf("set paid: %v", err)
			}
			wantPaid = wantPaid.Add(cost)
		} else {
			wantOpen = wantOpen.Add(cost)
		}
	}

	paid, open, err := st.YearPaymentTotals(ctx, yearID)
	if err != nil {
		t.Fatalf("YearPaymentTotals: %v", err)
	}
	if !paid.Equal(wantPaid) {
		t.Fatalf("paid = %s, want %s", paid, wantPaid)
	}
	if !open.Equal(wantOpen) {
		t.Fatalf("open = %s, want %s", open, wantOpen)
	}
}
