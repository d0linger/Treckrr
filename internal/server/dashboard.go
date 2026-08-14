package server

import (
	"errors"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// neighborSummary is a neighbor with its totals for the selected billing year.
type neighborSummary struct {
	Neighbor  models.Neighbor
	Cost      decimal.Decimal
	Hours     decimal.Decimal
	Entries   int
	Paid      bool // fully settled (nothing remaining)
	Remaining decimal.Decimal
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	// One query for the whole per-neighbor breakdown (net, hours, count, paid),
	// replacing a 2+3N round-trip fan-out. Totals are summed in memory below.
	summaryRows, err := s.store.YearNeighborSummaries(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "dashboard: year neighbor summaries", err)
		return
	}

	summaries := make([]neighborSummary, 0, len(summaryRows))
	var grandCost, grandHours, paidCost, openCost decimal.Decimal
	openCount := 0
	for _, row := range summaryRows {
		summaries = append(summaries, neighborSummary{
			Neighbor: models.Neighbor{ID: row.NeighborID, Name: row.Name},
			Cost:     row.Cost, Hours: row.Hours, Entries: row.Entries,
			Paid: row.Paid, Remaining: row.Remaining,
		})
		grandCost = grandCost.Add(row.Cost)
		grandHours = grandHours.Add(row.Hours)
		paidCost = paidCost.Add(row.PaidAmount) // actual money received
		if row.Remaining.IsPositive() {
			// "Offen" = what neighbors still owe (net minus payments). Negative
			// remainders (I owe them) are excluded from both count and sum so the
			// attention strip reports a consistent pair.
			openCost = openCost.Add(row.Remaining)
			openCount++
		}
	}

	available, err := s.store.ListNeighborsNotInYear(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "dashboard: available neighbors", err)
		return
	}

	data := s.newPage(w, r, "Übersicht", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, "dashboard: year selector", err)
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
			s.serverError(w, "dashboard: previous-year members", err)
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
	data["OpenCount"] = openCount
	// How many bookings are out of sync with the current basis (open years only).
	staleCount := 0
	if !year.Completed() {
		if rows, err := s.store.RecalcPreview(r.Context(), year.ID, nil); err == nil {
			for _, ro := range rows {
				if ro.Changed {
					staleCount++
				}
			}
		}
	}
	data["StaleCount"] = staleCount
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
		s.serverError(w, "add neighbor to year", err)
		return
	}
	s.audit(r, "add_neighbor", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
	s.setFlash(w, r, "success", "Nachbar zum Jahr hinzugefügt.")
	redirect(w, r, dashboardURL(yearID))
}

// handleYearRemoveNeighbor removes a neighbor from the year (membership only).
// It refuses when the neighbor still has entries booked or ledger postings in
// that year — removal would orphan them: still counted in the year total
// but invisible in the per-neighbor/payment views, the same
// skew the membership guard in handleLedgerAdd prevents on the add side.
func (s *Server) handleYearRemoveNeighbor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	count, err := s.store.CountEntriesForNeighborYear(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "remove neighbor: count entries", err)
		return
	}
	if count > 0 {
		s.setFlash(w, r, "error", "Nachbar hat noch Buchungen in diesem Jahr und kann nicht entfernt werden.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	ledgerCount, err := s.store.CountLedgerForNeighborYear(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "remove neighbor: count ledger", err)
		return
	}
	if ledgerCount > 0 {
		s.setFlash(w, r, "error", "Nachbar hat noch Verrechnungspositionen (Konto) in diesem Jahr und kann nicht entfernt werden.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	payCount, err := s.store.CountPaymentsForNeighborYear(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "remove neighbor: count payments", err)
		return
	}
	if payCount > 0 {
		s.setFlash(w, r, "error", "Nachbar hat noch Zahlungen in diesem Jahr und kann nicht entfernt werden.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if err := s.store.RemoveNeighborFromYear(r.Context(), yearID, neighborID); err != nil {
		s.serverError(w, "remove neighbor from year", err)
		return
	}
	s.audit(r, "remove_neighbor", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
	s.setFlash(w, r, "success", "Nachbar aus dem Jahr entfernt.")
	redirect(w, r, dashboardURL(yearID))
}

// handleNeighborUpdate changes a neighbor's name, note, address, and tax id.
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
	address := trimmed(r, "address")
	taxID := trimmed(r, "tax_id")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "Adresse", address, maxNoteLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "UID/Steuernummer", taxID, maxNameLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	before, _ := s.store.GetNeighbor(r.Context(), id)
	if err := s.store.UpdateNeighbor(r.Context(), id, name, note, address, taxID); err != nil {
		s.setFlash(w, r, "error", "Aktualisierung fehlgeschlagen.")
	} else {
		detail := name
		if before != nil {
			if d := diffFields(
				fieldChange{"Name", before.Name, name},
				fieldChange{"Notiz", before.Note, note},
				fieldChange{"Adresse", before.Address, address},
				fieldChange{"UID/Steuernr.", before.TaxID, taxID},
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
		s.serverError(w, "neighbor delete: count entries", err)
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

// handleNeighborAnonymize erases a neighbor's live personal data (DSGVO Art. 17)
// while keeping their bookings and the frozen invoice snapshots, which are under
// a legal retention obligation. Irreversible; audited.
func (s *Server) handleNeighborAnonymize(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	before, _ := s.store.GetNeighbor(r.Context(), id)
	if before != nil && before.Anonymized {
		s.setFlash(w, r, "error", "Nachbar ist bereits anonymisiert.")
	} else if err := s.store.AnonymizeNeighbor(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.setFlash(w, r, "error", "Anonymisieren fehlgeschlagen.")
	} else {
		detail := ""
		if before != nil {
			detail = before.Name + " → anonymisiert"
		}
		s.audit(r, "anonymize", "neighbor", id, detail)
		s.setFlash(w, r, "success", "Nachbar anonymisiert. Rechnungen bleiben aufbewahrungspflichtig erhalten.")
	}
	redirect(w, r, "/neighbors")
}
