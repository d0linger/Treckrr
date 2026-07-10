package server

import (
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

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
	data["Today"] = time.Now().Format("02.01.2006")
	s.render(w, r, "beleg", data)
}
