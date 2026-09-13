package models

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// LedgerBooking preserves the entered service separately from its signed account
// amount. A neighbor's service never becomes an outgoing invoice or own usage.
type LedgerBooking struct {
	Version       int             `json:"version"`
	Kind          string          `json:"kind"`
	TaskLabel     string          `json:"task_label"`
	Note          string          `json:"note,omitempty"`
	Unit          string          `json:"unit"`
	Quantity      decimal.Decimal `json:"quantity"`
	UnitPrice     decimal.Decimal `json:"unit_price"`
	PartnerLabel  string          `json:"partner_label,omitempty"`
	PartnerPerson string          `json:"partner_person,omitempty"`
	PersonHours   decimal.Decimal `json:"person_hours,omitempty"`
	PersonRate    decimal.Decimal `json:"person_rate,omitempty"`
}

// Total rounds each independently billed service before adding it, like entries.
func (b LedgerBooking) Total() decimal.Decimal {
	return b.Quantity.Mul(b.UnitPrice).Round(2).Add(b.PersonHours.Mul(b.PersonRate).Round(2))
}

// Summary keeps existing ledger exports and documents readable without decoding
// metadata, including both independent service calculations and the original note.
func (b LedgerBooking) Summary() string {
	parts := []string{b.TaskLabel}
	if b.PartnerLabel != "" {
		parts = append(parts, b.PartnerLabel)
	}
	parts = append(parts, fmt.Sprintf("%s %s × %s €", ledgerDecimal(b.Quantity), b.Unit, ledgerDecimal(b.UnitPrice)))
	if b.Kind == "labor" && b.PartnerPerson != "" {
		parts = append(parts, b.PartnerPerson)
	}
	if b.PersonHours.IsPositive() {
		parts = append(parts, fmt.Sprintf("%s: %s Mannstunden × %s €", b.PartnerPerson, ledgerDecimal(b.PersonHours), ledgerDecimal(b.PersonRate)))
	}
	if b.Note != "" {
		parts = append(parts, b.Note)
	}
	return strings.Join(parts, " · ")
}

// ledgerDecimal retains entered precision in German document descriptions.
func ledgerDecimal(value decimal.Decimal) string {
	return strings.ReplaceAll(value.String(), ".", ",")
}
