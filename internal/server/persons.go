package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// ---- Personenstamm, Mannstunden, Zuschläge (Ausbaukarte 56/57/58) ----------
//
// Mannstunden and Anfahrt are booked as ORDINARY entries with their own unit —
// cost stays quantity × unit price, so nothing in the pricing, recalc, invoice
// snapshot or export logic changes. What the master data adds is the rate: it
// comes from the Personenstamm resp. the Betriebsdaten instead of being
// retyped, correctly, on every booking.
//
// The unified booking form also routes own labor through the ordinary-entry
// model. These legacy endpoints remain available for existing callers and the
// dedicated travel surcharge; they do not reinterpret supplier counterclaims.

const (
	// Shared with the store, which books the same unit for a series' companion.
	unitMannstunde = models.UnitMannstunde
	unitAnfahrt    = "Anfahrt"
	unitKm         = "km"
)

// handlePersons renders the Personenstamm.
func (s *Server) handlePersons(w http.ResponseWriter, r *http.Request) {
	persons, err := s.store.ListPersons(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Personen", "persons")
	data["Persons"] = persons
	// Mannstunden je Person für das laufende Jahr (Nr. 56): the master data is
	// only half the feature — what the operator actually asks is who was out
	// how long. A farm without a billing year yet simply gets no table.
	if year, err := s.store.LatestBillingYear(r.Context()); err == nil {
		hours, err := s.store.PersonHoursForYear(r.Context(), year.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		data["Year"] = year
		data["PersonHours"] = hours
	}
	s.render(w, r, "persons", data)
}

// personForm reads and validates the shared name/rate/note fields.
func (s *Server) personForm(w http.ResponseWriter, r *http.Request) (name string, rate decimal.Decimal, note string, ok bool) {
	name = trimmed(r, "name")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		return "", rate, "", false
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		return "", rate, "", false
	}
	note = trimmed(r, "note")
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		return "", rate, "", false
	}
	if s.tooLong(w, r, "Stundensatz", r.FormValue("hourly_rate"), maxDecimalLen) {
		return "", rate, "", false
	}
	rate = formDecimal(r, "hourly_rate")
	if rate.IsNegative() {
		s.setFlash(w, r, "error", "Der Stundensatz darf nicht negativ sein.")
		return "", rate, "", false
	}
	return name, rate, note, true
}

// handlePersonCreate adds a helper to the master data.
func (s *Server) handlePersonCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	name, rate, note, ok := s.personForm(w, r)
	if !ok {
		redirect(w, r, "/personen")
		return
	}
	id, err := s.store.CreatePerson(r.Context(), name, rate, note)
	if err != nil {
		s.setFlash(w, r, "error", "Anlegen fehlgeschlagen (Name bereits vergeben?).")
	} else {
		s.audit(r, "create", "person", id, name+" · "+rate.StringFixed(2)+" €/h")
		s.setFlash(w, r, "success", "Person angelegt.")
	}
	redirect(w, r, "/personen")
}

// handlePersonUpdate saves a helper's name, rate and note.
func (s *Server) handlePersonUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	name, rate, note, ok := s.personForm(w, r)
	if !ok {
		redirect(w, r, "/personen")
		return
	}
	before, _ := s.store.GetPerson(r.Context(), id)
	if err := s.store.UpdatePerson(r.Context(), id, name, rate, note); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen (Name bereits vergeben?).")
	} else {
		detail := name
		if before != nil {
			if d := diffFields(
				fieldChange{"Name", before.Name, name},
				fieldChange{"Stundensatz", before.HourlyRate.StringFixed(2), rate.StringFixed(2)},
				fieldChange{"Notiz", before.Note, note},
			); d != "" {
				detail = d
			}
		}
		s.audit(r, "update", "person", id, detail)
		s.setFlash(w, r, "success", "Person aktualisiert.")
	}
	redirect(w, r, "/personen")
}

// handlePersonArchive hides or reactivates a helper.
func (s *Server) handlePersonArchive(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	archived := r.FormValue("archived") == "true"
	if err := s.store.SetPersonArchived(r.Context(), id, archived); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		action := "reactivate"
		msg := "Person reaktiviert."
		if archived {
			action, msg = "archive", "Person archiviert."
		}
		s.audit(r, action, "person", id, s.personName(r, id))
		s.setFlash(w, r, "success", msg)
	}
	redirect(w, r, "/personen")
}

// handlePersonDelete removes a helper who was never booked.
func (s *Server) handlePersonDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	name := s.personName(r, id)
	switch err := s.store.DeletePerson(r.Context(), id); {
	case errors.Is(err, store.ErrHasHistory):
		s.setFlash(w, r, "error", "Diese Person hat bereits Buchungen — bitte archivieren statt löschen.")
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	default:
		s.audit(r, "delete", "person", id, name)
		s.setFlash(w, r, "success", "Person gelöscht.")
	}
	redirect(w, r, "/personen")
}

// personName resolves a helper's name for audit lines and flashes.
func (s *Server) personName(r *http.Request, id int64) string {
	if p, err := s.store.GetPerson(r.Context(), id); err == nil {
		return p.Name
	}
	return fmt.Sprintf("#%d", id)
}

// bookExtra is the shared tail of the two surcharge bookings: it applies the
// same year/invoice lock the main booking form obeys and writes an ordinary
// entry, so the cost model is untouched.
func (s *Server) bookExtra(w http.ResponseWriter, r *http.Request, neighborID, yearID int64, e *models.Entry, auditDetail string) {
	back := neighborURL(neighborID, yearID)
	if !s.ledgerYearOpen(w, r, yearID, neighborID) {
		return
	}
	if in, err := s.store.NeighborInYear(r.Context(), yearID, neighborID); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	} else if !in {
		s.setFlash(w, r, "error", "Nachbar ist diesem Abrechnungsjahr nicht zugeordnet.")
		redirect(w, r, back)
		return
	}
	e.NeighborID = neighborID
	e.BillingYearID = yearID
	id, err := s.store.CreateEntry(r.Context(), e, nil)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "create", "entry", id, s.neighborName(r, neighborID)+" · "+auditDetail)
	s.setFlash(w, r, "success", "Buchung gespeichert.")
	redirect(w, r, back)
}

// handleMannstundenAdd books a helper's hours (Ausbaukarte 56/57): an ordinary
// entry with unit "Mannstunde", priced from the Personenstamm. An explicit rate
// on the form wins, so a one-off different rate stays possible without editing
// the master data.
func (s *Server) handleMannstundenAdd(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	back := neighborURL(neighborID, yearID)
	if s.tooLong(w, r, "Datum", r.FormValue("entry_date"), 50) ||
		s.tooLong(w, r, "Stunden", r.FormValue("hours"), maxDecimalLen) ||
		s.tooLong(w, r, "Stundensatz", r.FormValue("hourly_rate"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	personID := formInt64(r, "person_id")
	person, err := s.store.GetPerson(r.Context(), personID)
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Bitte eine Person wählen.")
		redirect(w, r, back)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	hours := formDecimal(r, "hours")
	if !hours.IsPositive() {
		s.setFlash(w, r, "error", "Stunden müssen größer als 0 sein.")
		redirect(w, r, back)
		return
	}
	rate := person.HourlyRate
	if override := formDecimal(r, "hourly_rate"); override.IsPositive() {
		rate = override
	}
	if !rate.IsPositive() {
		s.setFlash(w, r, "error", "Für diese Person ist kein Stundensatz hinterlegt — bitte einen Satz angeben.")
		redirect(w, r, back)
		return
	}
	note := trimmed(r, "note")
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, back)
		return
	}
	cost := hours.Mul(rate).Round(2)
	s.bookExtra(w, r, neighborID, yearID, &models.Entry{
		Date:      parsePaidOn(r.FormValue("entry_date")),
		TaskLabel: "Mannstunden " + person.Name,
		Unit:      unitMannstunde,
		Quantity:  hours,
		UnitPrice: rate,
		Cost:      cost,
		Note:      note,
		PersonID:  &person.ID,
	}, fmt.Sprintf("Mannstunden %s, %s h × %s = %s €",
		person.Name, hours.String(), rate.StringFixed(2), cost.StringFixed(2)))
}

// handleAnfahrtAdd books the Anfahrt surcharge (Ausbaukarte 58) from the
// Betriebsdaten: either the flat rate per Einsatz or km × Kilometergeld.
func (s *Server) handleAnfahrtAdd(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	back := neighborURL(neighborID, yearID)
	if s.tooLong(w, r, "Datum", r.FormValue("entry_date"), 50) ||
		s.tooLong(w, r, "Kilometer", r.FormValue("km"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	note := trimmed(r, "note")
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, back)
		return
	}
	date := parsePaidOn(r.FormValue("entry_date"))
	var e *models.Entry
	var detail string
	if strings.TrimSpace(r.FormValue("km")) != "" {
		km := formDecimal(r, "km")
		if !km.IsPositive() {
			s.setFlash(w, r, "error", "Die Kilometer müssen größer als 0 sein.")
			redirect(w, r, back)
			return
		}
		if !company.TravelPerKm.IsPositive() {
			s.setFlash(w, r, "error", "Es ist kein Kilometergeld hinterlegt (Betriebsdaten).")
			redirect(w, r, back)
			return
		}
		cost := km.Mul(company.TravelPerKm).Round(2)
		e = &models.Entry{
			Date: date, TaskLabel: "Anfahrt", Unit: unitKm,
			Quantity: km, UnitPrice: company.TravelPerKm, Cost: cost, Note: note,
		}
		detail = fmt.Sprintf("Anfahrt %s km × %s = %s €", km.String(), company.TravelPerKm.StringFixed(2), cost.StringFixed(2))
	} else {
		if !company.TravelFlat.IsPositive() {
			s.setFlash(w, r, "error", "Es ist keine Anfahrtspauschale hinterlegt (Betriebsdaten).")
			redirect(w, r, back)
			return
		}
		e = &models.Entry{
			Date: date, TaskLabel: "Anfahrtspauschale", Unit: unitAnfahrt,
			Quantity: decimal.NewFromInt(1), UnitPrice: company.TravelFlat, Cost: company.TravelFlat, Note: note,
		}
		detail = "Anfahrtspauschale " + company.TravelFlat.StringFixed(2) + " €"
	}
	s.bookExtra(w, r, neighborID, yearID, e, detail)
}
