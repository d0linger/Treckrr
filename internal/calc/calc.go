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
	"math"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// DaysBetween returns the whole-day difference from `from` to `to`, counted by
// calendar day in `from`'s location (both collapsed to local midnight): positive
// when `to` is later, negative when earlier, 0 for the same day. It rounds rather
// than truncating so a day that spans a DST transition (23h or 25h between two
// local midnights) still counts as one day. Used for invoice due-date countdowns
// and dunning overdue counts so both agree.
func DaysBetween(from, to time.Time) int {
	startFrom := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	startTo := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, to.Location())
	return int(math.Round(startTo.Sub(startFrom).Hours() / 24))
}

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
