package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// PriceTerm is a standing price agreement with one neighbor: a percentage off
// own catalog machine hours for a validity range (ValidTo nil = open-ended).
type PriceTerm struct {
	ID          int64
	NeighborID  int64
	Label       string
	DiscountPct decimal.Decimal
	ValidFrom   time.Time
	ValidTo     *time.Time
	Created     time.Time
}

// Covers reports whether the term applies on the given booking date.
func (t PriceTerm) Covers(date time.Time) bool {
	day := date.Format("2006-01-02")
	if day < t.ValidFrom.Format("2006-01-02") {
		return false
	}
	return t.ValidTo == nil || day <= t.ValidTo.Format("2006-01-02")
}

// Discounted returns the hourly rate after the term's discount, rounded to the
// cent like every stored rate, and the amount it took off per hour.
func (t PriceTerm) Discounted(rate decimal.Decimal) (decimal.Decimal, decimal.Decimal) {
	factor := decimal.NewFromInt(100).Sub(t.DiscountPct).Div(decimal.NewFromInt(100))
	discounted := rate.Mul(factor).Round(2)
	return discounted, rate.Sub(discounted)
}

// EffectivePriceTerm selects the term that applies on date: the one with the
// latest start among those covering it (newest wins on a tie).
func EffectivePriceTerm(terms []PriceTerm, date time.Time) *PriceTerm {
	var selected *PriceTerm
	for i := range terms {
		candidate := &terms[i]
		if !candidate.Covers(date) {
			continue
		}
		if selected == nil || candidate.ValidFrom.After(selected.ValidFrom) ||
			(candidate.ValidFrom.Equal(selected.ValidFrom) && candidate.ID > selected.ID) {
			selected = candidate
		}
	}
	return selected
}

// PriceTermNote is the line annotation for a booking billed under a term, as
// printed on the Beleg and frozen into invoice lines ("" without a term).
func PriceTermNote(label string, pct decimal.Decimal) string {
	if !pct.IsPositive() {
		return ""
	}
	return "abzgl. " + ledgerDecimal(pct) + " % " + label
}
