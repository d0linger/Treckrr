package models

import "github.com/shopspring/decimal"

// HasMeter reports whether the booking carries hour-meter readings.
func (e Entry) HasMeter() bool { return e.MeterStart.Valid && e.MeterEnd.Valid }

// MeterHours is the difference of the readings (zero without readings).
func (e Entry) MeterHours() decimal.Decimal {
	if !e.HasMeter() {
		return decimal.Zero
	}
	return e.MeterEnd.Decimal.Sub(e.MeterStart.Decimal)
}
