// Package calc implements the cost model derived from the source spreadsheet.
//
//	tractor hourly rate = PS * cost_per_PS(load level)
//	machine hourly rate = working width * cost_per_AB
//	gespann hourly rate = tractor rate + sum(machine rates)
//	entry cost          = hours * gespann hourly rate
//
// All arithmetic uses exact decimals (rounded to two places for currency) to
// avoid binary floating-point rounding drift in billing.
package calc

import (
	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// TractorRate returns the hourly rate for a tractor at a given load level.
func TractorRate(t models.Tractor, l models.LoadLevel) decimal.Decimal {
	return t.PS.Mul(l.CostPerPS).Round(2)
}

// MachineRate returns the hourly rate contribution of a machine.
func MachineRate(m models.Machine) decimal.Decimal {
	return m.WorkingWidth.Mul(m.CostPerAB).Round(2)
}

// GespannRate sums the tractor rate and all machine rates.
func GespannRate(t models.Tractor, l models.LoadLevel, machines []models.Machine) decimal.Decimal {
	rate := TractorRate(t, l)
	for _, m := range machines {
		rate = rate.Add(MachineRate(m))
	}
	return rate.Round(2)
}

// Cost multiplies hours by the hourly rate.
func Cost(hours, hourlyRate decimal.Decimal) decimal.Decimal {
	return hours.Mul(hourlyRate).Round(2)
}
