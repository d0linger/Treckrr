package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestFuelAdjustmentLifecycleIntegration covers migration 0077, effective-date
// selection, the pricing-freshness trigger and snapshot persistence on recalc.
func TestFuelAdjustmentLifecycleIntegration(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := context.Background()
	baseID, err := st.CreateEmptyBase(ctx, 2087, "Adjustment basis")
	if err != nil {
		t.Fatal(err)
	}
	loadID, err := st.CreateLoadLevel(ctx, baseID, "mittel", decimal.RequireFromString("0.36"), 1)
	if err != nil {
		t.Fatal(err)
	}
	tractorID, err := st.CreateTractor(ctx, baseID, "T1", "", decimal.RequireFromString("100"), 1)
	if err != nil {
		t.Fatal(err)
	}
	first := &models.FuelAdjustment{BaseID: baseID, EffectiveFrom: fuelDay(2087, 1, 1),
		Label: "Diesel", AmountPerH: decimal.RequireFromString("5")}
	if err := st.SaveFuelAdjustment(ctx, first); err != nil {
		t.Fatalf("save first adjustment: %v", err)
	}
	second := &models.FuelAdjustment{BaseID: baseID, EffectiveFrom: fuelDay(2087, 7, 1),
		Label: "Diesel Sommer", AmountPerH: decimal.RequireFromString("9")}
	if err := st.SaveFuelAdjustment(ctx, second); err != nil {
		t.Fatalf("save second adjustment: %v", err)
	}
	selected, err := st.FuelAdjustmentAt(ctx, baseID, fuelDay(2087, 6, 30))
	if err != nil || selected.ID != first.ID {
		t.Fatalf("June adjustment = %#v, %v; want first version", selected, err)
	}
	selected, err = st.FuelAdjustmentAt(ctx, baseID, fuelDay(2087, 7, 1))
	if err != nil || selected.ID != second.ID {
		t.Fatalf("July adjustment = %#v, %v; want second version", selected, err)
	}
	clonedBaseID, err := st.CloneBase(ctx, baseID, 2088, "Adjustment clone")
	if err != nil {
		t.Fatalf("clone basis: %v", err)
	}
	cloned, err := st.FuelAdjustmentAt(ctx, clonedBaseID, fuelDay(2088, 1, 1))
	if err != nil || cloned.Label != second.Label || !cloned.AmountPerH.Equal(second.AmountPerH) ||
		!cloned.EffectiveFrom.Equal(fuelDay(2088, 1, 1)) {
		t.Fatalf("cloned adjustment = %#v, %v; want current version rebased to 2088-01-01", cloned, err)
	}

	yearID, err := st.CreateBillingYear(ctx, 2087, baseID, "Adjustment year")
	if err != nil {
		t.Fatal(err)
	}
	neighborID, err := st.CreateNeighbor(ctx, "Adjustment neighbor", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, neighborID); err != nil {
		t.Fatal(err)
	}
	entryID, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: fuelDay(2087, 3, 1),
		TaskLabel: "Arbeit", TractorID: &tractorID, LoadLevelID: &loadID,
		TractorLabel: "T1 (100 PS)", LoadLabel: "mittel", Unit: "h",
		Hours: decimal.NewFromInt(2), Quantity: decimal.NewFromInt(2),
		HourlyRate: decimal.RequireFromString("41"), UnitPrice: decimal.RequireFromString("41"),
		FuelAdjustmentLabel: "Diesel", FuelAdjustmentPerH: decimal.RequireFromString("5"),
		Cost: decimal.RequireFromString("82"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first.AmountPerH = decimal.RequireFromString("7")
	if err := st.SaveFuelAdjustment(ctx, first); err != nil {
		t.Fatalf("update adjustment: %v", err)
	}
	if stale, err := st.CountPotentiallyStale(ctx, yearID, nil); err != nil || stale != 1 {
		t.Fatalf("stale count = %d, %v; want 1", stale, err)
	}
	preview, err := st.RecalcPreview(ctx, yearID, nil)
	if err != nil || len(preview) != 1 || !preview[0].NewRate.Equal(decimal.RequireFromString("43")) {
		t.Fatalf("preview = %#v, %v; want 43 €/h", preview, err)
	}
	if _, _, _, err := st.ApplyRecalc(ctx, yearID, nil); err != nil {
		t.Fatalf("apply adjustment: %v", err)
	}
	entry, err := st.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.FuelAdjustmentLabel != "Diesel" || !entry.FuelAdjustmentPerH.Equal(decimal.RequireFromString("7")) ||
		!entry.HourlyRate.Equal(decimal.RequireFromString("43")) || !entry.Cost.Equal(decimal.RequireFromString("86")) {
		t.Fatalf("recalculated snapshot = %+v", entry)
	}
}

func fuelDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
