//go:build integration

package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestMachineCostModelRequiresExplicitApply(t *testing.T) {
	ctx := context.Background()
	st, _ := scratchStore(t)
	baseID, err := st.CreateEmptyBase(ctx, 2026, "Cost model")
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateMachine(ctx, baseID, "Mäher", decimal.NewFromInt(2), decimal.NewFromInt(10), "", 0, decimal.NewFromInt(99))
	if err != nil {
		t.Fatal(err)
	}
	m := models.Machine{
		ID: id, AcquisitionCost: decimal.NewFromInt(50000), ResidualValue: decimal.NewFromInt(10000),
		UsefulYears: 10, AnnualHours: decimal.NewFromInt(500), FuelCostPerH: decimal.NewFromInt(8),
		AnnualMaintenance: decimal.NewFromInt(1000), AnnualInsurance: decimal.NewFromInt(500),
		AnnualOtherCost: decimal.NewFromInt(500),
	}
	if err := st.UpdateMachineCostModel(ctx, m, false); err != nil {
		t.Fatal(err)
	}
	got, err := st.ListMachines(ctx, baseID)
	if err != nil || len(got) != 1 {
		t.Fatalf("read model: %v, %d rows", err, len(got))
	}
	if !got[0].SelfCostPerH.Equal(decimal.NewFromInt(99)) {
		t.Fatalf("saving assumptions changed active rate to %s", got[0].SelfCostPerH)
	}
	if err := st.UpdateMachineCostModel(ctx, m, true); err != nil {
		t.Fatal(err)
	}
	got, err = st.ListMachines(ctx, baseID)
	if err != nil || !got[0].SelfCostPerH.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("explicit apply rate = %s, err=%v; want 20", got[0].SelfCostPerH, err)
	}
}
