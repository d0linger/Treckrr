package server

import (
	"net/http"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// neighborSummary is a neighbor with its totals for the selected billing year.
type neighborSummary struct {
	Neighbor models.Neighbor
	Cost     decimal.Decimal
	Hours    decimal.Decimal
	Entries  int
	Paid     bool
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	neighbors, err := s.store.ListYearNeighbors(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}

	payments, err := s.store.YearPayments(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}

	var summaries []neighborSummary
	var grandCost, grandHours, paidCost, openCost decimal.Decimal
	for _, n := range neighbors {
		cost, hours, err := s.store.NeighborTotal(r.Context(), n.ID, year.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		count, err := s.store.CountEntriesForNeighborYear(r.Context(), year.ID, n.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		// Net what the neighbor owes = work bookings + signed ledger postings.
		led, err := s.store.NeighborLedgerSum(r.Context(), year.ID, n.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		net := cost.Add(led)
		paid := payments[n.ID]
		summaries = append(summaries, neighborSummary{
			Neighbor: n, Cost: net, Hours: hours, Entries: count, Paid: paid,
		})
		grandCost = grandCost.Add(net)
		grandHours = grandHours.Add(hours)
		if paid {
			paidCost = paidCost.Add(net)
		} else {
			openCost = openCost.Add(net)
		}
	}

	available, err := s.store.ListNeighborsNotInYear(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}

	data := s.newPage(w, r, "Übersicht", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	// Offer "carry over neighbors from the previous year" when one exists and
	// there are members not yet in this year.
	if prev, err := s.store.PreviousBillingYear(r.Context(), year.Year); err == nil {
		current := map[int64]bool{}
		for _, sm := range summaries {
			current[sm.Neighbor.ID] = true
		}
		prevMembers, err := s.store.ListYearNeighbors(r.Context(), prev.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		var candidates []models.Neighbor
		for _, n := range prevMembers {
			if !current[n.ID] && !n.Archived {
				candidates = append(candidates, n)
			}
		}
		if len(candidates) > 0 {
			data["PrevYear"] = prev.Year
			data["PrevNeighbors"] = candidates
		}
	}
	data["Summaries"] = summaries
	data["Available"] = available
	data["GrandCost"] = grandCost
	data["GrandHours"] = grandHours
	data["Completed"] = year.Completed()
	data["PaidCost"] = paidCost
	data["OpenCost"] = openCost
	s.render(w, r, "dashboard", data)
}

// handleYearAddNeighbor adds an existing neighbor to the billing year.
func (s *Server) handleYearAddNeighbor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	if yearID == 0 || neighborID == 0 {
		s.setFlash(w, r, "error", "Bitte einen Nachbarn wählen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if err := s.store.AddNeighborToYear(r.Context(), yearID, neighborID); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.audit(r, "add_neighbor", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
	s.setFlash(w, r, "success", "Nachbar zum Jahr hinzugefügt.")
	redirect(w, r, dashboardURL(yearID))
}

// handleYearRemoveNeighbor removes a neighbor from the year (membership only).
// It refuses when the neighbor still has entries booked in that year.
func (s *Server) handleYearRemoveNeighbor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	count, err := s.store.CountEntriesForNeighborYear(r.Context(), yearID, neighborID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		s.setFlash(w, r, "error", "Nachbar hat noch Buchungen in diesem Jahr und kann nicht entfernt werden.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if err := s.store.RemoveNeighborFromYear(r.Context(), yearID, neighborID); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.audit(r, "remove_neighbor", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
	s.setFlash(w, r, "success", "Nachbar aus dem Jahr entfernt.")
	redirect(w, r, dashboardURL(yearID))
}

// handleNeighborPaid toggles the payment status of a neighbor within a year.
func (s *Server) handleNeighborPaid(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	if yearID == 0 || neighborID == 0 {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	paid := r.FormValue("paid") == "true"
	if err := s.store.SetNeighborPaid(r.Context(), yearID, neighborID, paid); err != nil {
		s.setFlash(w, r, "error", "Zahlungsstatus konnte nicht gesetzt werden.")
	} else if paid {
		s.audit(r, "mark_paid", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
		s.setFlash(w, r, "success", "Als bezahlt markiert.")
	} else {
		s.audit(r, "mark_open", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
		s.setFlash(w, r, "success", "Als offen markiert.")
	}
	redirect(w, r, dashboardURL(yearID))
}

func (s *Server) handleNeighborUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	name := trimmed(r, "name")
	note := trimmed(r, "note")
	before, _ := s.store.GetNeighbor(r.Context(), id)
	if err := s.store.UpdateNeighbor(r.Context(), id, name, note); err != nil {
		s.setFlash(w, r, "error", "Aktualisierung fehlgeschlagen.")
	} else {
		detail := name
		if before != nil {
			if d := diffFields(
				fieldChange{"Name", before.Name, name},
				fieldChange{"Notiz", before.Note, note},
			); d != "" {
				detail = d
			}
		}
		s.audit(r, "update", "neighbor", id, detail)
		s.setFlash(w, r, "success", "Nachbar aktualisiert.")
	}
	redirect(w, r, neighborReturnURL(r, id))
}

// neighborReturnURL points back to the central neighbor page when the request
// originated there, otherwise to the neighbor within the current year.
func neighborReturnURL(r *http.Request, id int64) string {
	if r.FormValue("origin") == "manage" {
		return "/neighbors"
	}
	return neighborURL(id, formInt64(r, "year_id"))
}

// handleNeighborDelete deletes the neighbor globally (all years and entries).
func (s *Server) handleNeighborDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Neighbors with bookings must not be deleted (would change history).
	// They can be deactivated instead.
	count, err := s.store.CountEntriesForNeighbor(r.Context(), id)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		s.setFlash(w, r, "error", "Nachbar hat Buchungen und kann nicht gelöscht werden. Bitte stattdessen deaktivieren.")
	} else {
		before, _ := s.store.GetNeighbor(r.Context(), id)
		if err := s.store.DeleteNeighbor(r.Context(), id); err != nil {
			s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
		} else {
			detail := ""
			if before != nil {
				detail = before.Name
			}
			s.audit(r, "delete", "neighbor", id, detail)
			s.setFlash(w, r, "success", "Nachbar gelöscht.")
		}
	}
	if r.FormValue("origin") == "manage" {
		redirect(w, r, "/neighbors")
		return
	}
	redirect(w, r, dashboardURL(s.yearIDFromForm(r)))
}
