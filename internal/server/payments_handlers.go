package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/store"
)

// neighborRemaining is the open balance for a neighbor in a year:
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
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
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
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	// Validate the optional Skonto up front (before recording anything): a
	// percentage in [0, 10]. Out of range rejects the whole submission.
	skonto := parseGermanDecimal(r.FormValue("skonto"))
	if skonto.IsNegative() || skonto.GreaterThan(decimal.NewFromInt(10)) {
		s.setFlash(w, r, "error", "Skonto muss zwischen 0 und 10 % liegen.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.AddPayment(r.Context(), yearID, neighborID, amount, parsePaidOn(r.FormValue("paid_on")), note); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	s.audit(r, "payment_add", "neighbor", neighborID,
		s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID)+" · "+amount.StringFixed(2)+" €")
	msg := "Zahlung erfasst."
	// Optional Skonto (§ 16 UStG): a percentage of the issued invoice's gross,
	// booked as a credit note (net + USt split) alongside the payment. Skipped when
	// there is no active invoice.
	if pct := skonto; pct.IsPositive() {
		iv, ierr := s.store.GetInvoice(r.Context(), yearID, neighborID)
		switch {
		case errors.Is(ierr, store.ErrNotFound):
			// no active invoice → a Skonto has nothing to reduce; ignore silently.
		case ierr != nil:
			s.setFlash(w, r, "error", "Zahlung erfasst, aber der Rechnungsstatus für das Skonto war nicht prüfbar.")
			redirect(w, r, neighborURL(neighborID, yearID))
			return
		case iv.Content != nil:
			skGross := iv.Content.Gross.Mul(pct).Div(decimal.NewFromInt(100)).Round(2)
			if skGross.IsPositive() {
				g, gerr := s.store.GutschriftInvoice(r.Context(), yearID, neighborID, skGross, "Skonto "+pct.String()+" %")
				if gerr != nil {
					s.setFlash(w, r, "error", "Zahlung erfasst, aber die Skonto-Gutschrift ist fehlgeschlagen (übersteigt sie den offenen Rechnungsbetrag?).")
					redirect(w, r, neighborURL(neighborID, yearID))
					return
				}
				s.audit(r, "invoice_gutschrift", "neighbor", neighborID,
					s.neighborName(r, neighborID)+" · Skonto "+g.Number+" · "+skGross.StringFixed(2)+" €")
				msg = "Zahlung + Skonto-Gutschrift " + g.Number + " (" + skGross.StringFixed(2) + " €) erfasst."
			}
		}
	}
	s.setFlash(w, r, "success", msg)
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handlePaymentDelete removes a payment and returns to its neighbor/year.
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
	deleted, err := s.store.DeletePayment(r.Context(), id)
	switch {
	case err != nil:
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	case deleted:
		s.audit(r, "payment_delete", "neighbor", p.NeighborID,
			s.neighborName(r, p.NeighborID)+" · "+p.Amount.StringFixed(2)+" €")
		s.setFlashUndo(w, r, "success", "Zahlung gelöscht.", "/payments/"+itoa64(id)+"/restore")
	default: // already deleted (e.g. a double-submit): no state change, no audit
		s.setFlash(w, r, "info", "Zahlung war bereits gelöscht.")
	}
	redirect(w, r, neighborURL(p.NeighborID, p.BillingYearID))
}

// handlePaymentRestore reverses a soft-deleted payment (the Undo action).
func (s *Server) handlePaymentRestore(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id) // by-id, sees soft-deleted rows
	if err != nil {
		http.NotFound(w, r)
		return
	}
	restored, err := s.store.RestorePayment(r.Context(), id)
	switch {
	case err != nil:
		s.setFlash(w, r, "error", "Wiederherstellen fehlgeschlagen.")
	case restored:
		s.audit(r, "payment_restore", "neighbor", p.NeighborID,
			s.neighborName(r, p.NeighborID)+" · "+p.Amount.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Zahlung wiederhergestellt.")
	default: // already active (e.g. a double-submit): no state change, no audit
		s.setFlash(w, r, "info", "Zahlung war bereits aktiv.")
	}
	redirect(w, r, neighborURL(p.NeighborID, p.BillingYearID))
}

// handleNeighborSettle records a payment for the exact remaining balance — the
// one-click "mark the rest as paid" action (replaces the old paid toggle).
func (s *Server) handleNeighborSettle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	if yearID == 0 || neighborID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
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
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
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
