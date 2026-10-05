package models

import (
	"time"

	"github.com/shopspring/decimal"
)

// Company holds invoice sender, tax, payment, dunning, travel, and mail defaults.
type Company struct {
	Name                string
	Address             string
	TaxID               string
	TaxNote             string
	TaxMode             string
	VATRate             decimal.Decimal
	IBAN                string
	EInvoiceStreet      string
	EInvoiceZIP         string
	EInvoiceTown        string
	EInvoiceCountryCode string
	PaymentTermDays     int
	DunningFee1         decimal.Decimal
	DunningFee2         decimal.Decimal
	DunningGraceDays    int
	SkontoPct           decimal.Decimal
	SkontoDays          int
	InvoicePrefix       string
	InvoiceStart        int
	SmallBusinessLimit  decimal.Decimal
	TravelFlat          decimal.Decimal
	TravelPerKm         decimal.Decimal
	MailSignature       string
	MailCC              string
}

// EffectiveTermDays returns the configured payment term or the legacy 14-day
// fallback. An explicit zero remains valid and means immediately due.
func (c Company) EffectiveTermDays() int {
	if c.PaymentTermDays < 0 {
		return 14
	}
	return c.PaymentTermDays
}

// Person is a helper with an own snapshottable hourly rate.
type Person struct {
	ID         int64
	Name       string
	HourlyRate decimal.Decimal
	Note       string
	Archived   bool
	Created    time.Time
}
