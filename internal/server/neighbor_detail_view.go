package server

import (
	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// neighborDetailView is the page-specific contract consumed by neighbor.html
// and its booking-form partial. Common layout and year-selector values remain
// in pageData while page families are migrated independently.
type neighborDetailView struct {
	Stale              map[int64]bool
	StaleCount         int
	TaskSummary        []taskSummary
	Completed          bool
	Base               *models.PriceBase
	Neighbor           *models.Neighbor
	Entries            []models.Entry
	BookingCount       int
	LinkedFrom         map[int64]int64
	PairLabel          map[int64]string
	TotalCost          decimal.Decimal
	TotalHours         decimal.Decimal
	Ledger             []models.LedgerEntry
	LedgerSum          decimal.Decimal
	Saldo              decimal.Decimal
	Payments           []models.Payment
	PaidSum            decimal.Decimal
	Installments       []installmentView
	Remaining          decimal.Decimal
	CreditAmount       decimal.Decimal
	HasInvoice         bool
	PhotoCounts        map[int64]int
	LedgerPhotoCounts  map[int64]int
	Photos             []store.PhotoRef
	Persons            []models.Person
	TravelFlat         decimal.Decimal
	TravelPerKm        decimal.Decimal
	HasTravelRates     bool
	Tractors           []models.Tractor
	Loads              []models.LoadLevel
	Machines           []models.Machine
	Gespanne           []models.Gespann
	Today              string
	BookingValues      map[string]string
	SelectedMachineIDs []int64
	BookingPrefilled   bool
	PrefilledMachine   *models.Machine
	BookingLocked      bool
	BookingAction      string
}

// bind adds the typed neighbor-detail contract to the common template map.
func (v neighborDetailView) bind(data pageData) {
	data["Stale"] = v.Stale
	data["StaleCount"] = v.StaleCount
	data["TaskSummary"] = v.TaskSummary
	data["Completed"] = v.Completed
	data["Base"] = v.Base
	data["Neighbor"] = v.Neighbor
	data["Entries"] = v.Entries
	data["BookingCount"] = v.BookingCount
	data["LinkedFrom"] = v.LinkedFrom
	data["PairLabel"] = v.PairLabel
	data["TotalCost"] = v.TotalCost
	data["TotalHours"] = v.TotalHours
	data["Ledger"] = v.Ledger
	data["LedgerSum"] = v.LedgerSum
	data["Saldo"] = v.Saldo
	data["Payments"] = v.Payments
	data["PaidSum"] = v.PaidSum
	data["Installments"] = v.Installments
	data["Remaining"] = v.Remaining
	data["CreditAmount"] = v.CreditAmount
	data["HasInvoice"] = v.HasInvoice
	data["PhotoCounts"] = v.PhotoCounts
	data["LedgerPhotoCounts"] = v.LedgerPhotoCounts
	data["Photos"] = v.Photos
	data["Persons"] = v.Persons
	data["TravelFlat"] = v.TravelFlat
	data["TravelPerKm"] = v.TravelPerKm
	data["HasTravelRates"] = v.HasTravelRates
	data["Tractors"] = v.Tractors
	data["Loads"] = v.Loads
	data["Machines"] = v.Machines
	data["Gespanne"] = v.Gespanne
	data["Today"] = v.Today
	data["BookingValues"] = v.BookingValues
	data["SelectedMachineIDs"] = v.SelectedMachineIDs
	data["BookingPrefilled"] = v.BookingPrefilled
	data["PrefilledMachine"] = v.PrefilledMachine
	data["BookingLocked"] = v.BookingLocked
	data["BookingAction"] = v.BookingAction
}
