package server

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// newBookingValues starts a new form without copying another party's rates.
func newBookingValues() map[string]string {
	return map[string]string{"booking_kind": "equipment", "booking_direction": "out", "mode": "gespann",
		"unit": "h", "entry_date": time.Now().Format("2006-01-02")}
}

// entryBookingKind recognizes historical free-name labor as well as linked people.
func entryBookingKind(entry *models.Entry) string {
	if entry.Unit == models.UnitMannstunde {
		return "labor"
	}
	if entry.Unit == "h" || entry.Unit == "" {
		return "equipment"
	}
	return "quantity"
}

// entryFormPeople preserves every linked component instead of choosing LIMIT 1.
func (s *Server) entryFormPeople(r *http.Request, entry *models.Entry) ([]models.BookingPerson, error) {
	people := []models.BookingPerson{}
	if entryBookingKind(entry) == "labor" {
		people = append(people, entryBookingPerson(*entry))
	}
	helpers, err := s.store.EntryCompanions(r.Context(), entry.ID)
	if err != nil {
		return nil, err
	}
	for _, helper := range helpers {
		people = append(people, entryBookingPerson(helper))
	}
	return people, nil
}

// setEntryBookingForm supplies the same controls for create, edit and copy.
func (s *Server) setEntryBookingForm(r *http.Request, data pageData, entry *models.Entry, copyMode bool) error {
	v := newBookingValues()
	v["booking_kind"], v["unit"] = entryBookingKind(entry), entry.Unit
	v["entry_date"], v["task_label"], v["note"] = entry.Date.Format("2006-01-02"), entry.TaskLabel, entry.Note
	v["hours"], v["quantity"], v["unit_price"] = entry.Hours.String(), entry.Quantity.String(), entry.UnitPrice.String()
	if entry.Unit == models.UnitMannstunde {
		v["hours"] = entry.Quantity.String()
	}
	if unitIsCustom(entry.Unit) {
		v["unit_custom"] = entry.Unit
	}
	v["mode"] = "manual"
	if entry.GespannID != nil {
		v["mode"], v["gespann_id"] = "gespann", strconv.FormatInt(*entry.GespannID, 10)
	}
	if entry.TractorID != nil {
		v["tractor_id"] = strconv.FormatInt(*entry.TractorID, 10)
	}
	if entry.LoadLevelID != nil {
		v["load_level_id"] = strconv.FormatInt(*entry.LoadLevelID, 10)
	}
	ids, err := s.store.EntryMachineIDs(r.Context(), entry.ID)
	if err != nil {
		return err
	}
	if v["booking_kind"] == "equipment" && entry.TractorID == nil && len(ids) == 0 {
		v["mode"], v["partner_label"], v["partner_rate"] = "free", entry.MachineLabels, entry.HourlyRate.String()
	}
	people, err := s.entryFormPeople(r, entry)
	if err != nil {
		return err
	}
	data["BookingAction"] = "/entries/" + strconv.FormatInt(entry.ID, 10) + "/update"
	if copyMode {
		data["BookingAction"] = "/entries"
		people = copyBookingPeople(people)
	}
	data["BookingValues"], data["BookingPeople"], data["SelectedMachineIDs"] = v, people, ids
	data["BookingHasOptionalPeople"] = optionalBookingPeople(v["booking_kind"], people)
	data["NeighborEquipment"], err = s.store.ListNeighborEquipment(r.Context(), entry.NeighborID)
	if err != nil {
		return err
	}
	data["BookingEdit"], data["BookingCopy"] = !copyMode, copyMode
	data["PhotoAction"], data["RecurringAction"] = "/entries/"+strconv.FormatInt(entry.ID, 10)+"/photos", "/entries/"+strconv.FormatInt(entry.ID, 10)+"/recur"
	data["BookingVoided"] = entry.Voided
	return nil
}

// copyBookingPeople excludes canceled work and discards source component IDs.
func copyBookingPeople(people []models.BookingPerson) []models.BookingPerson {
	copied := make([]models.BookingPerson, 0, len(people))
	for _, p := range people {
		if p.Voided {
			continue
		}
		p.ID = 0
		copied = append(copied, p)
	}
	return copied
}

// loadBookingCatalog supplies active catalogs for new work and historical
// choices for edits, so an archived person does not disappear from an old bill.
func (s *Server) loadBookingCatalog(r *http.Request, data pageData, year *models.BillingYear) error {
	var err error
	data["Tractors"], err = s.store.ListTractors(r.Context(), year.Base.ID)
	if err != nil {
		return err
	}
	data["Loads"], err = s.store.ListLoadLevels(r.Context(), year.Base.ID)
	if err != nil {
		return err
	}
	data["Machines"], err = s.store.ListMachines(r.Context(), year.Base.ID)
	if err != nil {
		return err
	}
	data["Gespanne"], err = s.store.ListGespanne(r.Context(), year.Base.ID)
	if err != nil {
		return err
	}
	data["Persons"], err = s.store.ListPersons(r.Context())
	data["Base"], data["Year"], data["Today"] = year.Base, year, time.Now().Format("2006-01-02")
	return err
}

// setLedgerBookingForm reads snapshots without inventing references for legacy names.
func (s *Server) setLedgerBookingForm(r *http.Request, data pageData, entry *models.LedgerEntry, year *models.BillingYear, neighborID int64, copyMode bool) error {
	if entry.Booking == nil {
		return nil
	}
	if err := s.loadBookingCatalog(r, data, year); err != nil {
		return err
	}
	b := entry.Booking
	v := newBookingValues()
	v["booking_kind"], v["booking_direction"] = b.Kind, "out"
	if entry.Amount.IsNegative() {
		v["booking_direction"] = "in"
	}
	v["mode"] = orDefault(b.Mode, "free")
	v["entry_date"], v["task_label"], v["note"] = entry.Date.Format("2006-01-02"), b.TaskLabel, b.Note
	v["unit"], v["quantity"], v["unit_price"], v["amount"] = b.Unit, b.Quantity.String(), b.UnitPrice.String(), b.UnitPrice.String()
	v["hours"], v["partner_rate"], v["partner_label"] = b.Quantity.String(), b.UnitPrice.String(), b.PartnerLabel
	if unitIsCustom(b.Unit) {
		v["unit_custom"] = b.Unit
	}
	if b.GespannID != nil {
		v["gespann_id"] = strconv.FormatInt(*b.GespannID, 10)
	}
	if b.TractorID != nil {
		v["tractor_id"] = strconv.FormatInt(*b.TractorID, 10)
	}
	if b.LoadLevelID != nil {
		v["load_level_id"] = strconv.FormatInt(*b.LoadLevelID, 10)
	}
	if b.NeighborEquipmentID != nil {
		v["neighbor_equipment_id"] = strconv.FormatInt(*b.NeighborEquipmentID, 10)
	}
	people := b.BookingPeople()
	data["BookingAction"] = "/ledger/" + strconv.FormatInt(entry.ID, 10) + "/update"
	if copyMode {
		data["BookingAction"] = "/entries"
		people = copyBookingPeople(people)
	}
	data["BookingValues"], data["BookingPeople"], data["SelectedMachineIDs"] = v, people, b.MachineIDs
	data["BookingHasOptionalPeople"] = optionalBookingPeople(b.Kind, people)
	equipment, err := s.store.ListNeighborEquipment(r.Context(), neighborID)
	if err != nil {
		return err
	}
	data["NeighborEquipment"] = equipment
	data["BookingEdit"], data["BookingCopy"], data["BookingVoided"] = !copyMode, copyMode, entry.Voided
	data["PhotoAction"], data["RecurringAction"] = "/ledger/"+strconv.FormatInt(entry.ID, 10)+"/photos", "/ledger/"+strconv.FormatInt(entry.ID, 10)+"/recur"
	data["NextWeek"] = time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	_, err = s.store.GetInvoice(r.Context(), year.ID, neighborID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	data["BookingLocked"] = year.Completed() || err == nil
	if !copyMode {
		data["Photos"], err = s.store.ListLedgerPhotos(r.Context(), entry.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func optionalBookingPeople(kind string, people []models.BookingPerson) bool {
	for i, person := range people {
		if person.Voided || (kind == "labor" && i == 0) {
			continue
		}
		return true
	}
	return false
}

// updateBookingEntryV2 edits all parts together; standalone helper links remain intact.
func (s *Server) updateBookingEntryV2(w http.ResponseWriter, r *http.Request, existing *models.Entry) {
	r.Form.Set("year_id", strconv.FormatInt(existing.BillingYearID, 10))
	r.Form.Set("neighbor_id", strconv.FormatInt(existing.NeighborID, 10))
	if trimmed(r, "booking_kind") != entryBookingKind(existing) || trimmed(r, "booking_direction") != "out" {
		s.rejectUnifiedBooking(w, r, "Art und Richtung bleiben beim Bearbeiten erhalten.")
		return
	}
	previous, err := s.entryFormPeople(r, existing)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	entry, ids, ledger, people, msg, err := s.parseBookingV2(r, previous, nil)
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	if msg != "" {
		s.rejectUnifiedBooking(w, r, msg)
		return
	}
	if ledger != nil || entry == nil {
		s.rejectUnifiedBooking(w, r, "Diese Buchung ist keine eigene Leistung.")
		return
	}
	entry.ID = existing.ID
	helpers := bookingHelpers(entry, people)
	if existing.LinkedEntryID != nil {
		if len(helpers) > 0 {
			s.rejectUnifiedBooking(w, r, "Weitere Personen bitte über die zugehörige Hauptbuchung hinzufügen.")
			return
		}
		err = s.store.UpdateEntry(r.Context(), entry, ids)
	} else {
		err = s.store.UpdateEntryGroup(r.Context(), entry, ids, helpers)
	}
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	s.setFlash(w, r, "success", "Buchung und Personen aktualisiert.")
	redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
}

// updateBookingLedgerV2 preserves direction and every independently priced component.
func (s *Server) updateBookingLedgerV2(w http.ResponseWriter, r *http.Request, existing *models.LedgerEntry, yearID, neighborID int64) {
	r.Form.Set("year_id", strconv.FormatInt(yearID, 10))
	r.Form.Set("neighbor_id", strconv.FormatInt(neighborID, 10))
	if trimmed(r, "booking_kind") != existing.Booking.Kind || (trimmed(r, "booking_direction") == "in") != existing.Amount.IsNegative() {
		s.rejectUnifiedBooking(w, r, "Art und Richtung bleiben beim Bearbeiten erhalten.")
		return
	}
	_, _, ledger, _, msg, err := s.parseBookingV2(r, existing.Booking.BookingPeople(), existing.Booking.NeighborEquipmentID)
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	if msg != "" {
		s.rejectUnifiedBooking(w, r, msg)
		return
	}
	if ledger == nil {
		s.rejectUnifiedBooking(w, r, "Diese Buchung ist eine Verrechnungsposition.")
		return
	}
	if err := s.store.UpdateLedgerBooking(r.Context(), existing.ID, *ledger); err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	s.setFlash(w, r, "success", "Buchung und Personen aktualisiert.")
	redirect(w, r, neighborURL(neighborID, yearID))
}
