package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Field is one of a neighbor's fields that bookings can name. AreaHa is
// zero when unknown.
type Field struct {
	ID         int64
	NeighborID int64
	Name       string
	AreaHa     decimal.Decimal
	PlotNumber string
	Archived   bool
	Created    time.Time
}

// Label is the field as shown in lists: name, area and Schlagnummer.
func (f Field) Label() string {
	label := f.Name
	if f.AreaHa.IsPositive() {
		label += " · " + ledgerDecimal(f.AreaHa) + " ha"
	}
	if f.PlotNumber != "" {
		label += " · Schlag " + f.PlotNumber
	}
	return label
}
