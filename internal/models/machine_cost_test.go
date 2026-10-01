package models

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestCalculatedSelfCost(t *testing.T) {
	m := Machine{
		AcquisitionCost: decimal.NewFromInt(50000), ResidualValue: decimal.NewFromInt(10000),
		UsefulYears: 10, AnnualHours: decimal.NewFromInt(500), FuelCostPerH: decimal.NewFromInt(8),
		AnnualMaintenance: decimal.NewFromInt(1000), AnnualInsurance: decimal.NewFromInt(500),
		AnnualOtherCost: decimal.NewFromInt(500), SelfCostPerH: decimal.NewFromInt(99),
	}
	got, ok := m.CalculatedSelfCost()
	if !ok || !got.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("CalculatedSelfCost() = %s, %v; want 20, true", got, ok)
	}
	if !m.SelfCostPerH.Equal(decimal.NewFromInt(99)) {
		t.Fatal("calculation changed the active self-cost rate")
	}
	m.UsefulYears = 0
	if _, ok := m.CalculatedSelfCost(); ok {
		t.Fatal("incomplete assumptions produced a proposal")
	}
}
