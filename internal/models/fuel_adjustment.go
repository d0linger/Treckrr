package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// FuelAdjustment is one effective-dated hourly addition to catalog equipment
// rates. Versions replace, rather than accumulate with, earlier versions.
type FuelAdjustment struct {
	ID            int64
	BaseID        int64
	EffectiveFrom time.Time
	Label         string
	AmountPerH    decimal.Decimal
	Created       time.Time
	Updated       time.Time
}
