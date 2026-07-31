package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/store"
)

// neighborRemaining is the open balance for a neighbour in a year:
// bookings + signed ledger − recorded payments.
func (s *Server) neighborRemaining(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	cost, _, err := s.store.NeighborTotal(ctx, neighborID, yearID)
	if err != nil {
		return decimal.Zero, err
	}
	ledger, err := s.store.NeighborLedgerSum(ctx, yearID, neighborID)
	if err != nil {
		return decimal.Zero, err
	}
	paid, err := s.store.NeighborPaymentSum(ctx, yearID, neighborID)
	if err != nil {
		return decimal.Zero, err
	}
	return cost.Add(ledger).Sub(paid), nil
}

// parsePaidOn parses the yyyy-mm-dd payment date, defaulting to today.
func parsePaidOn(v string) time.Time {
	if d, err := time.Parse("2006-01-02", strings.TrimSpace(v)); err == nil {
		return d
	}
	return time.Now()
}

// handlePaymentAdd records a dated payment. Unlike ledger postings, payments are
// allowed even after the year is completed — the payment side stays open until
// the balance is settled.
func (s *Server) handlePaymentAdd(w http.ResponseWriter, r *http.Request) {
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
	member, err := s.store.NeighborInYear(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "payment add: membership", err)
		return
	}
	if !member {
		s.setFlash(w, r, "error", "Nachbar ist in diesem Abrechnungsjahr nicht vorhanden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	amount, err := decimal.NewFromString(strings.ReplaceAll(strings.TrimSpace(r.FormValue("amount")), ",", "."))
	if err != nil || !amount.IsPositive() {
		s.setFlash(w, r, "error", "Bitte einen gültigen Betrag größer 0 eingeben.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if err := s.store.AddPayment(r.Context(), yearID, neighborID, amount, parsePaidOn(r.FormValue("paid_on")), note); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		s.audit(r, "payment_add", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID)+" · "+amount.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Zahlung erfasst.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handlePaymentDelete removes a payment and returns to its neighbour/year.
func (s *Server) handlePaymentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeletePayment(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "payment_delete", "neighbor", p.NeighborID,
			s.neighborName(r, p.NeighborID)+" · "+p.Amount.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Zahlung gelöscht.")
	}
	redirect(w, r, neighborURL(p.NeighborID, p.BillingYearID))
}

// handleNeighborSettle records a payment for the exact remaining balance — the
// one-click "mark the rest as paid" action (replaces the old paid toggle).
func (s *Server) handleNeighborSettle(w http.ResponseWriter, r *http.Request) {
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
	remaining, err := s.neighborRemaining(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "settle: remaining", err)
		return
	}
	if !remaining.IsPositive() {
		s.setFlash(w, r, "info", "Konto ist bereits ausgeglichen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if err := s.store.AddPayment(r.Context(), yearID, neighborID, remaining, time.Now(), "Restbetrag beglichen"); err != nil {
		s.setFlash(w, r, "error", "Zahlung konnte nicht verbucht werden.")
	} else {
		s.audit(r, "payment_settle", "year", yearID,
			s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID)+" · "+remaining.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Restbetrag als bezahlt verbucht.")
	}
	redirect(w, r, dashboardURL(yearID))
}

// handleNeighborCarryForward optionally moves the open balance into the next
// year (year+1) as a ledger transfer, settling the current year. Deliberate,
// button-triggered — never automatic.
func (s *Server) handleNeighborCarryForward(w http.ResponseWriter, r *http.Request) {
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
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.serverError(w, "carry: year", err)
		return
	}
	remaining, err := s.neighborRemaining(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "carry: remaining", err)
		return
	}
	if remaining.IsZero() {
		s.setFlash(w, r, "info", "Kein offener Rest zum Übernehmen.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	nextID, err := s.store.BillingYearIDForYear(r.Context(), year.Year+1)
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Kein Folgejahr angelegt.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err != nil {
		s.serverError(w, "carry: next year", err)
		return
	}
	member, err := s.store.NeighborInYear(r.Context(), nextID, neighborID)
	if err != nil {
		s.serverError(w, "carry: membership", err)
		return
	}
	if !member {
		s.setFlash(w, r, "error", "Nachbar ist im Folgejahr nicht vorhanden — dort zuerst hinzufügen.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	fromDesc := "Ins Folgejahr übertragen (" + itoa(year.Year+1) + ")"
	toDesc := "Übertrag aus " + itoa(year.Year)
	if err := s.store.CarryForward(r.Context(), neighborID, yearID, nextID, remaining, time.Now(), fromDesc, toDesc); err != nil {
		s.setFlash(w, r, "error", "Übernahme fehlgeschlagen.")
	} else {
		s.audit(r, "carry_forward", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · "+remaining.StringFixed(2)+" € → "+itoa(year.Year+1))
		s.setFlash(w, r, "success", "Rest ins Folgejahr übernommen.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}
