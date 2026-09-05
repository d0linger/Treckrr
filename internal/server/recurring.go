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
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if entry.Voided {
		s.setFlash(w, r, "error", "Aus einer stornierten Buchung kann keine Serie erstellt werden.")
		redirect(w, r, "/recurring")
		return
	}
	if s.tooLong(w, r, "Nächste Ausführung", r.FormValue("next_run"), maxNameLen) {
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
	}
	if err := s.store.CreateRecurring(r.Context(), entry.NeighborID, tmpl, kind, start); err != nil {
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
