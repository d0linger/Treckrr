package server

import (
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

// BelegRatePart is one component of an applied hourly rate — the tractor or a
// single machine — in the Kostengrundlage appendix.
type BelegRatePart struct {
	Label string
	Rate  decimal.Decimal
}

// BelegRate is one distinct applied hourly rate used in a beleg. When the basis
// still reproduces the frozen rate exactly, Parts itemizes it into the tractor
// and machine contributions; otherwise only the compact Title + Total are set.
type BelegRate struct {
	Title string
	Parts []BelegRatePart
	Total decimal.Decimal
}

// handleNeighborBeleg renders a compact, share-friendly statement for one
// neighbor and year (bookings + ledger + saldo) — a clean list to screenshot
// and hand over. Read-only; no actions, no editing.
func (s *Server) handleNeighborBeleg(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}

	entries, err := s.store.ListEntries(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "beleg: entries", err)
		return
	}
	cost, hours, err := s.store.NeighborTotal(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "beleg: total", err)
		return
	}
	ledger, err := s.store.ListNeighborLedger(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, "beleg: ledger", err)
		return
	}
	ledgerSum := decimal.Zero
	for _, l := range ledger {
		if !l.Voided {
			ledgerSum = ledgerSum.Add(l.Amount)
		}
	}

	// Basis items, keyed by id, to itemize the applied rates. Errors are
	// tolerated: a missing map just means the appendix falls back to the
	// compact (frozen) line for the affected combos.
	tractorByID := map[int64]models.Tractor{}
	loadByID := map[int64]models.LoadLevel{}
	machineByID := map[int64]models.Machine{}
	if ts, err := s.store.ListTractors(r.Context(), year.BaseID); err == nil {
		for _, t := range ts {
			tractorByID[t.ID] = t
		}
	}
	if ls, err := s.store.ListLoadLevels(r.Context(), year.BaseID); err == nil {
		for _, l := range ls {
			loadByID[l.ID] = l
		}
	}
	if ms, err := s.store.ListMachines(r.Context(), year.BaseID); err == nil {
		for _, m := range ms {
			machineByID[m.ID] = m
		}
	}

	// Distinct applied rates for the optional Kostengrundlage appendix. Each is
	// itemized into its tractor + machine parts from the basis, but only when
	// that reproduces the frozen rate exactly (so a later basis edit can never
	// show a misleading breakdown); otherwise the compact frozen line is kept.
	var rates []BelegRate
	bookings := 0
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Voided {
			continue
		}
		bookings++
		key := e.TractorLabel + "|" + e.LoadLabel + "|" + e.MachineLabels + "|" + e.HourlyRate.String()
		if seen[key] {
			continue
		}
		seen[key] = true

		title := e.TractorLabel
		for _, part := range []string{e.LoadLabel, e.MachineLabels} {
			if part == "" {
				continue
			}
			if title != "" {
				title += " · "
			}
			title += part
		}
		br := BelegRate{Title: title, Total: e.HourlyRate}

		if e.TractorID != nil && e.LoadLevelID != nil {
			if t, okT := tractorByID[*e.TractorID]; okT {
				if l, okL := loadByID[*e.LoadLevelID]; okL {
					if mids, err := s.store.EntryMachineIDs(r.Context(), e.ID); err == nil && len(mids) > 0 {
						parts := []BelegRatePart{{Label: "Traktor " + t.Label() + " · " + l.Name, Rate: calc.TractorRate(t, l)}}
						sum := calc.TractorRate(t, l)
						resolved := true
						for _, mid := range mids {
							m, okM := machineByID[mid]
							if !okM {
								resolved = false
								break
							}
							mr := calc.MachineRate(m)
							parts = append(parts, BelegRatePart{Label: m.Name, Rate: mr})
							sum = sum.Add(mr)
						}
						if resolved && sum.Round(2).Equal(e.HourlyRate) {
							br.Parts = parts
						}
					}
				}
			}
		}
		rates = append(rates, br)
	}

	// Populate the basis (name + year) for the appendix header.
	if year.Base == nil {
		if b, err := s.store.GetBase(r.Context(), year.BaseID); err == nil {
			year.Base = b
		}
	}
	// Payment status is only tracked once a year is completed.
	paid := false
	if year.Completed() {
		payments, err := s.store.YearPayments(r.Context(), year.ID)
		if err != nil {
			s.serverError(w, "beleg: payments", err)
			return
		}
		paid = payments[neighbor.ID]
	}

	data := s.newPage(w, r, neighbor.Name+" · Beleg", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, "beleg: year selector", err)
		return
	}
	data["Neighbor"] = neighbor
	data["Entries"] = entries
	data["TotalCost"] = cost
	data["TotalHours"] = hours
	data["Ledger"] = ledger
	data["LedgerSum"] = ledgerSum
	data["Saldo"] = cost.Add(ledgerSum)
	data["Completed"] = year.Completed()
	data["Paid"] = paid
	data["Rates"] = rates
	data["Bookings"] = bookings
	data["ShowGrund"] = r.URL.Query().Get("grundlage") == "1"
	data["Today"] = time.Now().Format("02.01.2006")
	s.render(w, r, "beleg", data)
}
