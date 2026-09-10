package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// handleRecurringList shows all recurring-booking rules.
func (s *Server) handleRecurringList(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListRecurring(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Wiederkehrende Buchungen", "dashboard")
	data["Rules"] = rules
	data["Today"] = time.Now().Format("2006-01-02")
	s.render(w, r, "recurring", data)
}

// handleRecurringCreate turns an existing booking into a recurring rule: the
// booking's fields become the template; the operator picks cadence + start date.
func (s *Server) handleRecurringCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Check the raw value before trimming or reading the booking. The request
	// body is already capped by limitBody; this is a field-level ceiling.
	if s.tooLong(
		w,
		r,
		"Startdatum",
		r.FormValue("next_run"),
		maxNameLen,
	) {
		redirect(w, r, "/recurring")
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// A storno is a statement that this booking should not have been made. Copying
	// it into a series would recreate it on every run — verified before this guard
	// existed: voiding a 42,00 € booking and setting up a weekly series produced an
	// active rule carrying the full amount. The form is hidden for voided bookings
	// (entry_edit.html), so reaching this means a stale page or a crafted request.
	if entry.Voided {
		s.setFlash(w, r, "error", "Aus einer stornierten Buchung kann keine Serie eingerichtet werden.")
		redirect(w, r, "/recurring")
		return
	}
	machineIDs, err := s.store.EntryMachineIDs(r.Context(), id)
	if err != nil {
		// Don't save a template that would silently drop the entry's machines.
		s.serverError(w, r.URL.Path, err)
		return
	}
	kind := r.FormValue("interval_kind")
	if kind != "weekly" && kind != "monthly" {
		kind = "weekly"
	}
	start, perr := time.Parse("2006-01-02", trimmed(r, "next_run"))
	if perr != nil {
		start = time.Now().AddDate(0, 0, 7)
	}
	tmpl := models.RecurTemplate{
		Unit: entry.Unit, Quantity: entry.Quantity, UnitPrice: entry.UnitPrice,
		Hours: entry.Hours, HourlyRate: entry.HourlyRate, Cost: entry.Cost,
		GespannID: entry.GespannID, TractorID: entry.TractorID, LoadLevelID: entry.LoadLevelID, MachineIDs: machineIDs,
		TractorLabel: entry.TractorLabel, LoadLabel: entry.LoadLabel, MachineLabels: entry.MachineLabels,
		TaskLabel: entry.TaskLabel, Note: entry.Note,
		// A series made from a Mannstunden booking keeps booking it for that
		// helper — the attribution is part of the booking, not decoration.
		PersonID: entry.PersonID,
	}
	// A booking made together with a helper repeats WITH the helper unless the
	// operator says otherwise on the form: the pair is the work as it happens
	// every week, and a series that quietly drops half of it would understate
	// every occurrence. The rate is frozen from the companion actually booked,
	// so a one-off rate on the source booking is what the series repeats.
	if r.FormValue("with_person") == "1" && (entry.Unit == "" || entry.Unit == "h") {
		partnerID, perr := s.store.LinkedPartnerID(r.Context(), id)
		if perr != nil {
			s.serverError(w, r.URL.Path, perr)
			return
		}
		if partnerID != 0 {
			partner, gerr := s.store.GetEntry(r.Context(), partnerID)
			if gerr != nil && !errors.Is(gerr, store.ErrNotFound) {
				// Reading the partner failed for a real reason. Creating the rule
				// anyway would freeze a helper-less template the operator cannot
				// correct afterwards (UpdateRecurring keeps templates frozen), so
				// this fails loudly instead of quietly building the wrong series.
				s.serverError(w, r.URL.Path, gerr)
				return
			}
			// A voided companion is a statement that those hours should not have
			// been booked — the same reason a voided SOURCE cannot start a series.
			if gerr == nil && partner.PersonID != nil && partner.UnitPrice.IsPositive() && !partner.Voided {
				person, err := s.store.GetPerson(r.Context(), *partner.PersonID)
				if errors.Is(err, store.ErrNotFound) {
					s.setFlash(w, r, "error", "Die verknüpfte Person ist nicht mehr vorhanden. Bitte die Buchung neu laden.")
					redirect(w, r, "/recurring")
					return
				} else if err != nil {
					s.serverError(w, r.URL.Path, err)
					return
				}
				tmpl.Companion = &models.RecurCompanion{
					PersonID: *partner.PersonID,
					Name:     person.Name,
					Rate:     partner.UnitPrice,
				}
			}
		}
	}
	if err := s.store.CreateRecurring(r.Context(), id, entry.NeighborID, tmpl, kind, start); err != nil {
		if errors.Is(err, store.ErrSourceEntryVoided) {
			s.setFlash(w, r, "error", "Aus einer stornierten Buchung kann keine Serie eingerichtet werden.")
			redirect(w, r, "/recurring")
			return
		}
		if errors.Is(err, store.ErrSourceCompanionUnavailable) {
			s.setFlash(w, r, "error", "Die verknüpften Mannstunden wurden geändert, storniert oder gelöscht. Bitte die Buchung neu laden.")
			redirect(w, r, "/recurring")
			return
		}
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "recurring_create", "neighbor", entry.NeighborID, tmpl.Summary()+" · "+kind)
	s.setFlash(w, r, "success", "Serie eingerichtet.")
	redirect(w, r, "/recurring")
}

// handleRecurringToggle pauses/resumes a rule.
func (s *Server) handleRecurringToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	active, err := s.store.ToggleRecurring(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Audit the state change: pausing/resuming a rule is a data modification like
	// create/delete, so it belongs in the trail (it was the one modify handler missing it).
	state := "pausiert"
	if active {
		state = "aktiviert"
	}
	s.audit(r, "recurring_toggle", "recurring", id, state)
	redirect(w, r, "/recurring")
}

// handleRecurringDelete removes a rule (created bookings stay).
func (s *Server) handleRecurringDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighborID, err := s.store.DeleteRecurring(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "recurring_delete", "recurring", id, s.neighborName(r, neighborID)+" · Serie entfernt")
	s.setFlash(w, r, "success", "Serie entfernt.")
	redirect(w, r, "/recurring")
}

// handleRecurringUpdate changes a rule's cadence and next run (Ausbaukarte 68).
func (s *Server) handleRecurringUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if s.tooLong(
		w,
		r,
		"Startdatum",
		r.FormValue("next_run"),
		maxNameLen,
	) {
		redirect(w, r, "/recurring")
		return
	}
	kind := r.FormValue("interval_kind")
	next, perr := time.Parse("2006-01-02", trimmed(r, "next_run"))
	if perr != nil {
		s.setFlash(w, r, "error", "Bitte ein gültiges Datum für den nächsten Lauf angeben.")
		redirect(w, r, "/recurring")
		return
	}
	switch err := s.store.UpdateRecurring(r.Context(), id, kind, next); {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	default:
		s.audit(r, "update", "recurring", id, kind+" · nächster Lauf "+next.Format("02.01.2006"))
		s.setFlash(w, r, "success", "Serie aktualisiert.")
	}
	redirect(w, r, "/recurring")
}

// handleRecurringRunNow books one extra occurrence for today (Ausbaukarte 68).
func (s *Server) handleRecurringRunNow(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entryID, booked, err := s.store.RunRecurringNow(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(err, store.ErrInactiveRule):
		s.setFlash(w, r, "error", "Die Serie ist pausiert — bitte zuerst aktivieren.")
	case err != nil:
		s.serverError(w, r.URL.Path, err)
		return
	case !booked:
		s.setFlash(w, r, "error", "Für heute gibt es kein offenes Abrechnungsjahr, dem dieser Nachbar zugeordnet ist.")
	case entryID == 0:
		// The idempotency key already existed: today's occurrence is there.
		s.setFlash(w, r, "info", "Für heute wurde bereits eine Buchung dieser Serie erstellt.")
	default:
		s.audit(r, "run_now", "recurring", id, "eine Buchung für heute erstellt")
		s.setFlash(w, r, "success", "Buchung für heute erstellt.")
	}
	redirect(w, r, "/recurring")
}
