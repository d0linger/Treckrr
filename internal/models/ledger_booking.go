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
	// Catalog references are descriptive: incoming equipment never creates own usage.
	Mode        string          `json:"mode,omitempty"`
	GespannID   *int64          `json:"gespann_id,omitempty"`
	TractorID   *int64          `json:"tractor_id,omitempty"`
	LoadLevelID *int64          `json:"load_level_id,omitempty"`
	MachineIDs  []int64         `json:"machine_ids,omitempty"`
	PersonID    *int64          `json:"person_id,omitempty"`
	People      []BookingPerson `json:"people,omitempty"`
	// NeighborEquipmentID links to reusable foreign equipment. The adjacent
	// fields are immutable booking snapshots used when the master data changes.
	NeighborEquipmentID   *int64          `json:"neighbor_equipment_id,omitempty"`
	EquipmentCapacity     decimal.Decimal `json:"equipment_capacity,omitempty"`
	EquipmentCapacityUnit string          `json:"equipment_capacity_unit,omitempty"`
	EquipmentBillingUnit  string          `json:"equipment_billing_unit,omitempty"`
}

// Total rounds each independently billed service before adding it, like entries.
func (b LedgerBooking) Total() decimal.Decimal {
	total := b.Quantity.Mul(b.UnitPrice).Round(2)
	if b.Kind == "labor" {
		total = decimal.Zero
	}
	for _, person := range b.BookingPeople() {
		total = total.Add(person.Cost())
	}
	return total
}

// BookingPeople reads both current component lists and historical single-person
// snapshots without rewriting old bookings or guessing a master-data identity.
func (b LedgerBooking) BookingPeople() []BookingPerson {
	if len(b.People) > 0 {
		return b.People
	}
	if b.Kind == "labor" {
		return []BookingPerson{{ID: 1, PersonID: b.PersonID, Name: b.PartnerPerson,
			Hours: b.Quantity, Rate: b.UnitPrice}}
	}
	if b.PersonHours.IsPositive() {
		return []BookingPerson{{ID: 1, PersonID: b.PersonID, Name: b.PartnerPerson,
			Hours: b.PersonHours, Rate: b.PersonRate}}
	}
	return nil
}

// Summary keeps existing ledger exports and documents readable without decoding
// metadata, including both independent service calculations and the original note.
func (b LedgerBooking) Summary() string {
	parts := []string{b.TaskLabel}
	if b.PartnerLabel != "" {
		parts = append(parts, b.PartnerLabel)
	}
	if len(b.People) == 0 || b.Kind != "labor" {
		parts = append(parts, fmt.Sprintf("%s %s × %s €", ledgerDecimal(b.Quantity), b.Unit, ledgerDecimal(b.UnitPrice)))
	}
	if len(b.People) == 0 && b.Kind == "labor" {
		if b.PartnerPerson != "" {
			parts = append(parts, b.PartnerPerson)
		}
	} else {
		for _, person := range b.BookingPeople() {
			line := fmt.Sprintf("%s: %s Mannstunden × %s €", person.Name, ledgerDecimal(person.Hours), ledgerDecimal(person.Rate))
			if person.Voided {
				line += " (storniert)"
			}
			parts = append(parts, line)
		}
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
