//go:build integration

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
	"github.com/d0linger/treckrr/internal/money"
	"github.com/d0linger/treckrr/internal/store"
)

// TestRecalcKeepsFreeEquipmentPriceIntegration ensures an hourly equipment
// booking with an explicitly agreed rate and no catalog IDs is not mistaken for
// a catalog-backed machines-only booking and reset to zero.
func TestRecalcKeepsFreeEquipmentPriceIntegration(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	yearNumber := 4500 + os.Getpid()%1000
	neighborName := fmt.Sprintf("Free-Equipment-Nachbar %d", os.Getpid())
	purgeFixtures(t, ctx, pool, fixtures{Years: []int{yearNumber}, NeighborNames: []string{neighborName}})

	baseID, err := st.CreateEmptyBase(ctx, yearNumber, "Free-Equipment-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yearNumber, baseID, "Free-Equipment-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	neighborID, err := st.CreateNeighbor(ctx, neighborName, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	defer purgeRootsByID(t, ctx, pool, []int64{yearID}, []int64{neighborID}, []int64{baseID})
	if err := st.AddNeighborToYear(ctx, yearID, neighborID); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	hours := decimal.NewFromInt(2)
	rate := decimal.NewFromInt(25)
	entryID, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Fremdgerät",
		MachineLabels: "Fremdgerät", Unit: "h", Quantity: hours, UnitPrice: rate,
		Hours: hours, HourlyRate: rate, Cost: money.Amount(hours, rate),
	}, nil)
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}

	rows, err := st.RecalcPreview(ctx, yearID, &neighborID)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(rows) != 1 || rows[0].Changed || rows[0].NewRate.StringFixed(2) != "25.00" ||
		rows[0].NewCost.StringFixed(2) != "50.00" {
		t.Fatalf("free equipment preview = %+v, want unchanged 25.00/50.00", rows)
	}
	updated, _, _, err := st.ApplyRecalc(ctx, yearID, &neighborID)
	if err != nil || updated != 0 {
		t.Fatalf("apply: updated=%d err=%v, want no change", updated, err)
	}
	entry, err := st.GetEntry(ctx, entryID)
	if err != nil || entry.HourlyRate.StringFixed(2) != "25.00" || entry.Cost.StringFixed(2) != "50.00" {
		t.Fatalf("stored free equipment = %+v err=%v, want unchanged", entry, err)
	}
}
