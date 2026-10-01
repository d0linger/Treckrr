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
	"github.com/d0linger/treckrr/internal/money"
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
	return money.Amount(t.PS, l.CostPerPS)
}

// MachineRate returns the hourly rate contribution of a machine.
func MachineRate(m models.Machine) decimal.Decimal {
	return money.Amount(m.WorkingWidth, m.CostPerAB)
}

// RateBreakdown is the calculated hourly price of an equipment combination.
// Each contribution is rounded before HourlyRate is summed, matching the
// spreadsheet and the historical billing behavior. MachineRates has the same
// order as the machines supplied to NewRateBreakdown.
type RateBreakdown struct {
	TractorRate  decimal.Decimal
	MachineRates []decimal.Decimal
	HourlyRate   decimal.Decimal
}

// NewRateBreakdown calculates the individually rounded contributions and their
// combined hourly rate. A tractor and load level form one pricing component;
// callers remain responsible for rejecting a half-set pair.
func NewRateBreakdown(t *models.Tractor, l *models.LoadLevel, machines []models.Machine) RateBreakdown {
	breakdown := RateBreakdown{
		MachineRates: make([]decimal.Decimal, 0, len(machines)),
	}
	if t != nil && l != nil {
		breakdown.TractorRate = TractorRate(*t, *l)
		breakdown.HourlyRate = breakdown.TractorRate
	}
	for _, machine := range machines {
		rate := MachineRate(machine)
		breakdown.MachineRates = append(breakdown.MachineRates, rate)
		breakdown.HourlyRate = breakdown.HourlyRate.Add(rate)
	}
	breakdown.HourlyRate = breakdown.HourlyRate.Round(2)
	return breakdown
}

// GespannRate sums the tractor rate and all machine rates.
//
// A nil tractor or load level contributes nothing: a rig may be machines only,
// for work where the customer supplies the tractor and only the implement is
// billed (the ÖKL list prices implements separately for exactly that reason).
//
// The two are all-or-nothing. TractorRate is PS × cost-per-PS, so a tractor
// without a load level has no rate to compute; callers must reject that pairing
// rather than let it silently drop the tractor from the price. The signature
// takes pointers so every call site had to be revisited when this changed.
func GespannRate(t *models.Tractor, l *models.LoadLevel, machines []models.Machine) decimal.Decimal {
	return NewRateBreakdown(t, l, machines).HourlyRate
}

// Cost multiplies hours by the hourly rate.
func Cost(hours, hourlyRate decimal.Decimal) decimal.Decimal {
	return money.Amount(hours, hourlyRate)
}
