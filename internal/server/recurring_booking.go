package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// recurringTemplateCompanions freezes every active component, including free names.
func (s *Server) recurringTemplateCompanions(r *http.Request, sourceID int64) ([]models.RecurCompanion, error) {
	entries, err := s.store.EntryCompanions(r.Context(), sourceID)
	if err != nil {
		return nil, err
	}
	companions := []models.RecurCompanion{}
	for _, entry := range entries {
		if entry.Voided {
			continue
		}
		name := entry.PersonName
		var personID int64
		if entry.PersonID != nil {
			personID = *entry.PersonID
			if name == "" {
				person, err := s.store.GetPerson(r.Context(), personID)
				if err != nil {
					return nil, err
				}
				name = person.Name
			}
		}
		companions = append(companions, models.RecurCompanion{
			EntryID: entry.ID, PersonID: personID, Name: name,
			Hours: entry.Quantity, Rate: entry.UnitPrice,
		})
	}
	return companions, nil
}

// handleLedgerRecurringCreate repeats a frozen counterclaim without own usage.
func (s *Server) handleLedgerRecurringCreate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	if s.tooLong(w, r, "Startdatum", r.FormValue("next_run"), maxNameLen) {
		redirect(w, r, "/recurring")
		return
	}
	start, err := time.Parse("2006-01-02", trimmed(r, "next_run"))
	if err != nil {
		s.setFlash(w, r, "error", "Bitte ein gültiges Startdatum angeben.")
		redirect(w, r, "/recurring")
		return
	}
	kind := trimmed(r, "interval_kind")
	if kind != "weekly" && kind != "monthly" {
		s.badRequest(w, "Unbekannter Rhythmus.")
		return
	}
	err = s.store.CreateLedgerRecurring(r.Context(), store.LedgerRecurringInput{
		SourceID: id, IncludePeople: r.FormValue("with_person") == "1", IntervalKind: kind, NextRun: start,
	})
	if errors.Is(err, store.ErrSourceEntryVoided) {
		s.setFlash(w, r, "error", "Aus einer stornierten Buchung kann keine Serie eingerichtet werden.")
		redirect(w, r, "/recurring")
		return
	}
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	s.audit(r, "recurring_create", "ledger", id, "Serie eingerichtet · "+kind)
	s.setFlash(w, r, "success", "Serie eingerichtet.")
	redirect(w, r, "/recurring")
}
