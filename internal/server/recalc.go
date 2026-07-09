package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"treckrr/internal/store"
)

// recalcPreview renders the before/after table for re-pricing a year's bookings
// (optionally one neighbor) against the current basis. Blocked on a completed
// year. neighborID nil = whole year.
func (s *Server) recalcPreview(w http.ResponseWriter, r *http.Request, yearID int64, neighborID *int64, title, backURL, applyURL string) {
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, backURL)
		return
	}
	rows, err := s.store.RecalcPreview(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "recalc preview", err)
		return
	}
	var oldTotal, newTotal decimal.Decimal
	count := 0
	for _, ro := range rows {
		if ro.Changed {
			count++
			oldTotal = oldTotal.Add(ro.OldCost)
			newTotal = newTotal.Add(ro.NewCost)
		}
	}
	// Warn when the change touches already-settled neighbors. Surface a lookup
	// failure rather than silently dropping the warning.
	payments, err := s.store.YearPayments(r.Context(), yearID)
	if err != nil {
		s.serverError(w, "recalc preview: payments", err)
		return
	}
	paid := false
	if neighborID != nil {
		paid = payments[*neighborID]
	} else {
		for _, p := range payments {
			if p {
				paid = true
				break
			}
		}
	}
	data := s.newPage(w, r, "Neu berechnen", "dashboard")
	data["Scope"] = title
	data["YearScope"] = neighborID == nil
	data["YearID"] = yearID
	data["YearLabel"] = year.Year
	data["Rows"] = rows
	data["Count"] = count
	data["OldTotal"] = oldTotal
	data["NewTotal"] = newTotal
	data["Diff"] = newTotal.Sub(oldTotal)
	data["Paid"] = paid
	data["ApplyURL"] = applyURL
	data["BackURL"] = backURL
	s.render(w, r, "recalc", data)
}

// recalcApply applies the recalculation and audits the result.
func (s *Server) recalcApply(w http.ResponseWriter, r *http.Request, yearID int64, neighborID *int64, entity string, entityID int64, backURL string) {
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, backURL)
		return
	}
	updated, oldTotal, newTotal, err := s.store.ApplyRecalc(r.Context(), yearID, neighborID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrYearCompleted):
			s.setFlash(w, r, "error", "Das Abrechnungsjahr wurde zwischenzeitlich abgeschlossen.")
			redirect(w, r, backURL)
		case errors.Is(err, store.ErrRecalcConflict):
			s.setFlash(w, r, "error", "Eine Buchung wurde zwischenzeitlich geändert – bitte erneut prüfen.")
			redirect(w, r, backURL)
		default:
			s.serverError(w, "recalc apply", err)
		}
		return
	}
	if updated == 0 {
		s.setFlash(w, r, "info", "Keine Buchung war zu ändern – bereits auf dem Stand der Grundlage.")
		redirect(w, r, backURL)
		return
	}
	diff := newTotal.Sub(oldTotal)
	scope := "Jahr " + s.yearLabel(r, yearID)
	if neighborID != nil {
		scope = s.neighborName(r, *neighborID) + " · Jahr " + s.yearLabel(r, yearID)
	}
	s.audit(r, "recalc", entity, entityID,
		fmt.Sprintf("%s · %d Buchungen · Δ %s €", scope, updated, diff.StringFixed(2)))
	s.setFlash(w, r, "success", fmt.Sprintf("%d Buchungen neu berechnet (Δ %s €).", updated, diff.StringFixed(2)))
	redirect(w, r, backURL)
}

func (s *Server) handleNeighborRecalcPreview(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID := formInt64(r, "year") // link uses ?year= like the rest of the neighbor flow
	if yearID == 0 {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	back := neighborURL(neighborID, yearID)
	s.recalcPreview(w, r, yearID, &neighborID, s.neighborName(r, neighborID), back,
		fmt.Sprintf("/neighbors/%d/recalc", neighborID))
}

func (s *Server) handleNeighborRecalcApply(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	s.recalcApply(w, r, yearID, &neighborID, "neighbor", neighborID, neighborURL(neighborID, yearID))
}

func (s *Server) handleYearRecalcPreview(w http.ResponseWriter, r *http.Request) {
	yearID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.recalcPreview(w, r, yearID, nil, "", dashboardURL(yearID), fmt.Sprintf("/years/%d/recalc", yearID))
}

func (s *Server) handleYearRecalcApply(w http.ResponseWriter, r *http.Request) {
	yearID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.recalcApply(w, r, yearID, nil, "year", yearID, dashboardURL(yearID))
}
