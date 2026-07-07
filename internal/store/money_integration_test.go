package store_test

import (
	"context"
	"os"
	"testing"

	"github.com/shopspring/decimal"

	"treckrr/internal/db"
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
