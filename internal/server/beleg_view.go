package server

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// belegView is the typed document contract shared by the authenticated receipt
// page and its read-only public portal. Shell, selector, and portal controls are
// bound separately because they are delivery concerns rather than document data.
type belegView struct {
	Neighbor              *models.Neighbor
	Days                  []BelegDay
	HasCalculationPath    bool
	Groups                []BelegService
	CanBundle             bool
	Bundle                bool
	TotalCost             decimal.Decimal
	TotalHours            decimal.Decimal
	Ledger                []models.LedgerEntry
	LedgerSum             decimal.Decimal
	Saldo                 decimal.Decimal
	Completed             bool
	Paid                  bool
	Credit                bool
	Payments              []models.Payment
	PaidSum               decimal.Decimal
	Remaining             decimal.Decimal
	HasPayments           bool
	Company               models.Company
	HasInvoice            bool
	Invoice               models.Invoice
	EInvoiceMissing       []string
	Rechnung              bool
	InvIssuer             models.InvoiceParty
	InvRecipient          models.InvoiceParty
	InvTaxNote            string
	InvIBAN               string
	InvHash               string
	InvShowVAT            bool
	InvRate               decimal.Decimal
	InvNet                decimal.Decimal
	InvUSt                decimal.Decimal
	InvBrutto             decimal.Decimal
	HasEpcQR              bool
	InvLedger             decimal.Decimal
	InvPaidUSt            decimal.Decimal
	Documents             []models.Invoice
	HasDocuments          bool
	Anzahlungen           []models.Invoice
	AnzahlungSum          decimal.Decimal
	TodayISO              string
	InvCredits            decimal.Decimal
	HasCredits            bool
	InvRest               decimal.Decimal
	SkontoUntil           time.Time
	SkontoPct             decimal.Decimal
	DueOn                 time.Time
	DueDays               int
	OverdueDays           int
	InvNeedRecipientVATID bool
	GrundTractors         []BelegTractor
	GrundMachines         []BelegMachine
	GrundCatalogReference bool
	HasGrund              bool
	Bookings              int
	ShowGrund             bool
	Today                 string
}

func (v belegView) bind(data pageData) {
	data["Neighbor"] = v.Neighbor
	data["Days"] = v.Days
	data["HasCalculationPath"] = v.HasCalculationPath
	data["Groups"] = v.Groups
	data["CanBundle"] = v.CanBundle
	data["Bundle"] = v.Bundle
	data["TotalCost"] = v.TotalCost
	data["TotalHours"] = v.TotalHours
	data["Ledger"] = v.Ledger
	data["LedgerSum"] = v.LedgerSum
	data["Saldo"] = v.Saldo
	data["Completed"] = v.Completed
	data["Paid"] = v.Paid
	data["Credit"] = v.Credit
	data["Payments"] = v.Payments
	data["PaidSum"] = v.PaidSum
	data["Remaining"] = v.Remaining
	data["HasPayments"] = v.HasPayments
	data["Company"] = v.Company
	data["HasInvoice"] = v.HasInvoice
	data["Invoice"] = v.Invoice
	data["EInvoiceMissing"] = v.EInvoiceMissing
	data["Rechnung"] = v.Rechnung
	data["InvIssuer"] = v.InvIssuer
	data["InvRecipient"] = v.InvRecipient
	data["InvTaxNote"] = v.InvTaxNote
	data["InvIBAN"] = v.InvIBAN
	data["InvHash"] = v.InvHash
	data["InvShowVAT"] = v.InvShowVAT
	data["InvRate"] = v.InvRate
	data["InvNet"] = v.InvNet
	data["InvUSt"] = v.InvUSt
	data["InvBrutto"] = v.InvBrutto
	data["HasEpcQR"] = v.HasEpcQR
	data["InvLedger"] = v.InvLedger
	data["InvPaidUSt"] = v.InvPaidUSt
	data["Documents"] = v.Documents
	data["HasDocuments"] = v.HasDocuments
	data["Anzahlungen"] = v.Anzahlungen
	data["AnzahlungSum"] = v.AnzahlungSum
	data["TodayISO"] = v.TodayISO
	data["InvCredits"] = v.InvCredits
	data["HasCredits"] = v.HasCredits
	data["InvRest"] = v.InvRest
	data["SkontoUntil"] = v.SkontoUntil
	data["SkontoPct"] = v.SkontoPct
	data["DueOn"] = v.DueOn
	data["DueDays"] = v.DueDays
	data["OverdueDays"] = v.OverdueDays
	data["InvNeedRecipientVATID"] = v.InvNeedRecipientVATID
	data["GrundTractors"] = v.GrundTractors
	data["GrundMachines"] = v.GrundMachines
	data["GrundCatalogReference"] = v.GrundCatalogReference
	data["HasGrund"] = v.HasGrund
	data["Bookings"] = v.Bookings
	data["ShowGrund"] = v.ShowGrund
	data["Today"] = v.Today
}
