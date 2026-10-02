package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// neighborRemaining is the payable balance used by every cash action. Once an
// invoice exists this is its frozen gross (plus credits/ledger, less payments),
// otherwise it falls back to the live booking net.
func (s *Server) neighborRemaining(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	return s.settlements().PayableRemaining(ctx, yearID, neighborID)
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
		s.notFound(w, r)
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
	// Bounded before parsing: this call does not go through parseGermanDecimal, so
	// it does not inherit that guard, and it runs BEFORE every other check in this
	// handler — making it the cheapest field to abuse, not the safest.
	if s.tooLong(w, r, "Betrag", r.FormValue("amount"), maxDecimalLen) {
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	// parseGermanDecimalOK carries the exponent/length guards (see there) —
	// this used to be one of three hand-rolled copies of them.
	amount, okAmount := parseGermanDecimalOK(r.FormValue("amount"))
	if !okAmount || !amount.IsPositive() {
		s.setFlash(w, r, "error", "Bitte einen gültigen Betrag größer 0 eingeben.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if models.HasSubCent(amount) {
		s.setFlash(w, r, "error", msgMoneyCents)
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if s.tooLong(w, r, "Datum", r.FormValue("paid_on"), 50) {
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if s.tooLong(w, r, "Skonto", r.FormValue("skonto"), maxDecimalLen) {
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
	key := strings.TrimSpace(r.FormValue("idempotency_key"))
	if s.tooLong(w, r, "Formularkennung", key, maxNameLen) {
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	// Payment and optional Skonto (§ 16 UStG: a percentage of the issued
	// invoice's gross, booked as a credit note with net + USt split) are one
	// transaction: either both are recorded or neither. No active invoice means
	// the Skonto has nothing to reduce and is ignored, as before.
	res, err := s.store.RecordPayment(r.Context(), store.PaymentInput{
		YearID: yearID, NeighborID: neighborID, Amount: amount,
		PaidOn: parsePaidOn(r.FormValue("paid_on")), Note: note, Method: paymentMethod(r),
		SkontoPct: skonto, IdempotencyKey: key,
	})
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		s.setFlash(w, r, "error", msgFormKeyConflict)
	case errors.Is(err, store.ErrSkontoYearCompleted):
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen — eine Skonto-Gutschrift ist nicht mehr möglich. Nichts gespeichert: Zahlung ohne Skonto erfassen oder das Jahr wieder öffnen.")
	case errors.Is(err, store.ErrGutschriftTooLarge):
		s.setFlash(w, r, "error", "Das Skonto übersteigt den noch nicht gutgeschriebenen Rechnungsbetrag. Nichts gespeichert.")
	case err != nil:
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	case res.Duplicate:
		// A resubmitted form (lost redirect, browser retry): the payment is
		// already there, and it must not be booked a second time.
		s.setFlash(w, r, "info", "Diese Zahlung war bereits erfasst.")
	case res.Skonto != nil:
		s.setFlash(w, r, "success", "Zahlung + Skonto-Gutschrift "+res.Skonto.Number+" ("+
			res.Skonto.Content.Gross.Neg().StringFixed(2)+" €) erfasst.")
	default:
		s.setFlash(w, r, "success", "Zahlung erfasst.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// User-facing messages shared by the money forms.
const (
	msgMoneyCents         = "Bitte einen Betrag mit höchstens zwei Nachkommastellen eingeben."
	msgFormKeyConflict    = "Dieses Formular wurde bereits mit anderen Angaben gespeichert — bitte die Seite neu laden und erneut erfassen."
	msgLedgerTransferEdit = "Ein Übertrag kann nicht einseitig geändert werden — bitte den Übertrag als Ganzes stornieren oder rückgängig machen und neu übernehmen."
)

// handlePaymentDelete removes a payment and returns to its neighbor/year.
func (s *Server) handlePaymentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	deleted, err := s.store.DeletePayment(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrImportedPaymentImmutable):
		s.setFlash(w, r, "error", "Importierte Zahlungen werden im Importjournal durch eine Gegenbuchung korrigiert.")
	case err != nil:
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	case deleted:
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
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id) // by-id, sees soft-deleted rows
	if err != nil {
		s.notFound(w, r)
		return
	}
	restored, err := s.store.RestorePayment(r.Context(), id)
	switch {
	case err != nil:
		s.setFlash(w, r, "error", "Wiederherstellen fehlgeschlagen.")
	case restored:
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
	// The amount is derived from the balance, so it must be read and written in
	// one locked transaction — see store.SettleRemaining. Reading it here and
	// passing it down let two concurrent clicks book the same rest twice.
	booked, err := s.store.SettleRemaining(r.Context(), yearID, neighborID, time.Now(), "Restbetrag beglichen")
	switch {
	case err != nil:
		s.setFlash(w, r, "error", "Zahlung konnte nicht verbucht werden.")
	case booked.IsZero():
		s.setFlash(w, r, "info", "Konto ist bereits ausgeglichen.")
	default:
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
		s.notFound(w, r)
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
	// Advisory fast path only: it spares the year/membership lookups below when
	// there is plainly nothing to move. The amount that actually gets posted is
	// recomputed under the account lock in store.CarryForwardRemaining — this
	// value must never reach the write.
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
	moved, err := s.store.CarryForwardRemaining(r.Context(), neighborID, yearID, nextID, time.Now(), fromDesc, toDesc)
	switch {
	case errors.Is(err, store.ErrCarryTargetCompleted):
		s.setFlash(w, r, "error", "Das Folgejahr "+itoa(year.Year+1)+" ist bereits abgeschlossen — ein Übertrag dorthin ist nicht möglich.")
	case err != nil:
		s.setFlash(w, r, "error", "Übernahme fehlgeschlagen.")
	case moved.IsZero():
		s.setFlash(w, r, "info", "Kein offener Rest zum Übernehmen.")
	default:
		s.setFlash(w, r, "success", "Rest ins Folgejahr übernommen.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// paymentMethod whitelists the Zahlungsart. Anything unknown collapses to "" —
// the field feeds displays and exports, never money math, so a typo must not be
// able to invent a category.
func paymentMethod(r *http.Request) string {
	switch v := r.FormValue("method"); v {
	case "überweisung", "bar", "verrechnung":
		return v
	}
	return ""
}

// handlePaymentEditForm renders the correction form for one payment. Payments
// were the only money record without an edit path: bookings and ledger rows have
// edit/void/undo, a mistyped payment forced delete-and-retype (Ausbaukarte 38).
func (s *Server) handlePaymentEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if p.ImportBatchID != nil || p.Reversal {
		s.setFlash(w, r, "error", "Importierte Zahlungen werden im Importjournal durch eine Gegenbuchung korrigiert.")
		redirect(w, r, neighborURL(p.NeighborID, p.BillingYearID))
		return
	}
	data := s.newPage(w, r, "Zahlung bearbeiten", "")
	data["Payment"] = p
	data["NeighborName"] = s.neighborName(r, p.NeighborID)
	data["Back"] = neighborURL(p.NeighborID, p.BillingYearID)
	s.render(w, r, "payment_edit", data)
}

// handlePaymentUpdate saves the correction.
func (s *Server) handlePaymentUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	p, err := s.store.GetPayment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	back := neighborURL(p.NeighborID, p.BillingYearID)
	if s.tooLong(
		w,
		r,
		"Datum",
		r.FormValue("paid_on"),
		50,
	) {
		redirect(w, r, back)
		return
	}
	if s.tooLong(w, r, "Betrag", r.FormValue("amount"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	amount, okAmount := parseGermanDecimalOK(r.FormValue("amount"))
	if !okAmount || !amount.IsPositive() {
		s.setFlash(w, r, "error", "Bitte einen gültigen Betrag größer 0 eingeben.")
		redirect(w, r, back)
		return
	}
	if models.HasSubCent(amount) {
		s.setFlash(w, r, "error", msgMoneyCents)
		redirect(w, r, back)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, back)
		return
	}
	updated, err := s.store.UpdatePayment(r.Context(), id, amount, parsePaidOn(r.FormValue("paid_on")), note, paymentMethod(r))
	if errors.Is(err, store.ErrImportedPaymentImmutable) {
		s.setFlash(w, r, "error", "Importierte Zahlungen werden im Importjournal durch eine Gegenbuchung korrigiert.")
		redirect(w, r, back)
		return
	}
	if err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
		redirect(w, r, back)
		return
	}
	// The row is soft-deleted (GetPayment sees those, the UPDATE does not), so
	// nothing changed. Auditing and flashing success here would record an edit
	// that never happened — and a later Restore would bring back the old amount.
	if !updated {
		s.setFlash(w, r, "error", "Diese Zahlung ist gelöscht — bitte zuerst wiederherstellen.")
		redirect(w, r, back)
		return
	}
	s.setFlash(w, r, "success", "Zahlung aktualisiert.")
	redirect(w, r, back)
}

// installmentView is one Ratenplan row with its derived state.
type installmentView struct {
	Plan   models.PaymentPlan
	Status string
}

// installmentViews derives each installment's state by comparing the paid sum
// against the cumulative plan: money is not earmarked per rate — whatever has
// been paid covers the plan from the top. "erledigt" once the paid sum reaches
// the running total, "überfällig" past the due date, "offen" otherwise.
func installmentViews(plans []models.PaymentPlan, paid decimal.Decimal) []installmentView {
	if len(plans) == 0 {
		return nil
	}
	out := make([]installmentView, 0, len(plans))
	cum := decimal.Zero
	today := time.Now().Format("2006-01-02")
	for _, p := range plans {
		cum = cum.Add(p.Amount)
		status := "offen"
		switch {
		case paid.GreaterThanOrEqual(cum):
			status = "erledigt"
		case p.DueOn.Format("2006-01-02") < today:
			status = "überfällig"
		}
		out = append(out, installmentView{Plan: p, Status: status})
	}
	return out
}

// handleInstallmentAdd records one agreed installment (Ratenplan, Ausbaukarte 43).
func (s *Server) handleInstallmentAdd(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	back := neighborURL(neighborID, yearID)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if s.tooLong(
		w,
		r,
		"Datum",
		r.FormValue("due_on"),
		50,
	) {
		redirect(w, r, back)
		return
	}
	if s.tooLong(w, r, "Betrag", r.FormValue("amount"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	amount, okAmount := parseGermanDecimalOK(r.FormValue("amount"))
	if !okAmount || !amount.IsPositive() {
		s.setFlash(w, r, "error", "Bitte einen gültigen Betrag größer 0 eingeben.")
		redirect(w, r, back)
		return
	}
	if models.HasSubCent(amount) {
		s.setFlash(w, r, "error", msgMoneyCents)
		redirect(w, r, back)
		return
	}
	note := strings.TrimSpace(r.FormValue("note"))
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, back)
		return
	}
	key := strings.TrimSpace(r.FormValue("idempotency_key"))
	if s.tooLong(w, r, "Formularkennung", key, maxNameLen) {
		redirect(w, r, back)
		return
	}
	_, duplicate, err := s.store.AddInstallmentOnce(r.Context(), yearID, neighborID, amount,
		parsePaidOn(r.FormValue("due_on")), note, key)
	switch {
	case errors.Is(err, store.ErrIdempotencyConflict):
		s.setFlash(w, r, "error", msgFormKeyConflict)
		redirect(w, r, back)
		return
	case err != nil:
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
		redirect(w, r, back)
		return
	case duplicate:
		s.setFlash(w, r, "info", "Diese Rate war bereits erfasst.")
		redirect(w, r, back)
		return
	}
	s.audit(r, "installment_add", "neighbor", neighborID,
		s.neighborName(r, neighborID)+" · Rate "+amount.StringFixed(2)+" €")
	s.setFlash(w, r, "success", "Rate hinzugefügt.")
	redirect(w, r, back)
}

// handleInstallmentDelete removes one installment.
func (s *Server) handleInstallmentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	p, err := s.store.DeleteInstallment(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "installment_delete", "neighbor", p.NeighborID,
		s.neighborName(r, p.NeighborID)+" · Rate "+p.Amount.StringFixed(2)+" € entfernt")
	s.setFlash(w, r, "success", "Rate entfernt.")
	redirect(w, r, neighborURL(p.NeighborID, p.BillingYearID))
}

// handleCreditPayout books the cash-out of a credit balance (Ausbaukarte 41): a
// positive ledger posting that neutralizes the negative rest, with the payout
// named as such. Cash leaving the farm and a mutual offset both end the credit —
// what differs is only the description trail.
func (s *Server) handleCreditPayout(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	back := neighborURL(neighborID, yearID)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	// Deliberately NOT restricted to open years: paying out a Guthaben is a
	// settlement, like "Rest als bezahlt" and the carry-forward, and completed
	// years are exactly where the dashboard asks for it. The store serializes
	// it on the account lock and audits it (store.PayoutCredit).
	// The amount is derived from the balance, so it must be recomputed under the
	// account lock inside the writing transaction — reading it here and posting
	// it afterwards is the read-then-write race that lets two concurrent clicks
	// pay the same credit out twice (see store.PayoutCredit).
	amount, err := s.store.PayoutCredit(r.Context(), yearID, neighborID, time.Now(), "Guthaben ausbezahlt")
	if err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
		redirect(w, r, back)
		return
	}
	if amount.IsZero() {
		s.setFlash(w, r, "info", "Kein Guthaben vorhanden.")
		redirect(w, r, back)
		return
	}
	s.setFlash(w, r, "success", "Guthaben von "+amount.StringFixed(2)+" € als ausbezahlt verbucht.")
	redirect(w, r, back)
}
