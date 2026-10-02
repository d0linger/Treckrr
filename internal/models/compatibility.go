package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// NeighborEquipment is read-only compatibility data for export and erasure of
// records created by the retired neighbor-specific equipment implementation.
// New booking code must use the shared price-basis Machine catalog.
type NeighborEquipment struct {
	ID           int64           `json:"id"`
	NeighborID   int64           `json:"neighbor_id"`
	Name         string          `json:"name"`
	Capacity     decimal.Decimal `json:"capacity"`
	CapacityUnit string          `json:"capacity_unit"`
	BillingUnit  string          `json:"billing_unit"`
	DefaultRate  decimal.Decimal `json:"default_rate"`
	Note         string          `json:"note,omitempty"`
	Archived     bool            `json:"archived"`
	Created      time.Time       `json:"created_at"`
}
