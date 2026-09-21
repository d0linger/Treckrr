package models

import "github.com/shopspring/decimal"

// BookingPerson freezes one person's attribution and independently agreed price.
// ID identifies a component within its booking, never a master-data person.
type BookingPerson struct {
	ID       int64           `json:"id,omitempty"`
	PersonID *int64          `json:"person_id,omitempty"`
	Name     string          `json:"name"`
	Hours    decimal.Decimal `json:"hours"`
	Rate     decimal.Decimal `json:"rate"`
	Voided   bool            `json:"voided,omitempty"`
}

// Cost rounds each active person's line independently of the machine charge.
func (p BookingPerson) Cost() decimal.Decimal {
	if p.Voided {
		return decimal.Zero
	}
	return p.Hours.Mul(p.Rate).Round(2)
}
