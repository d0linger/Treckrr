package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// unifiedBookingSelection accepts only declared form variants. Empty fields are
// the historical equipment/quantity form, so old offline queues remain valid.
func unifiedBookingSelection(r *http.Request) (kind, direction, msg string) {
	kind, direction = trimmed(r, "booking_kind"), trimmed(r, "booking_direction")
	if kind == "" {
		kind = "equipment"
		if unit := trimmed(r, "unit"); unit != "" && unit != "h" {
			kind = "quantity"
		}
	}
	if direction == "" {
		direction = "out"
	}
	validKind := kind == "equipment" || kind == "labor" || kind == "quantity" || kind == "fixed"
	if !validKind || (direction != "out" && direction != "in") {
		return "", "", "Bitte eine gültige Buchungsart und Richtung wählen."
	}
	if trimmed(r, "booking_kind") == "quantity" && trimmed(r, "unit") == "h" {
		return "", "", "Maschinenstunden bitte als Traktor / Gespann / Gefährt erfassen."
	}
	return kind, direction, ""
}

// positiveBookingDecimal rejects malformed, excessively precise or overflowing
// numeric input before decimal multiplication and the numeric database boundary.
func positiveBookingDecimal(r *http.Request, field string) (decimal.Decimal, bool) {
	raw := strings.ReplaceAll(trimmed(r, field), ",", ".")
	if len(raw) == 0 || len(raw) > 24 || strings.ContainsAny(raw, "eE") {
		return decimal.Zero, false
	}
	v, err := decimal.NewFromString(raw)
	if err != nil || !v.IsPositive() || v.Exponent() < -4 || v.GreaterThanOrEqual(decimal.NewFromInt(1_000_000_000)) {
		return decimal.Zero, false
	}
	return v, true
}

// ledgerBookingFromForm builds an independently priced counterclaim snapshot.
// Equipment supplied by a neighbor uses its agreed rate, never our price basis.
func ledgerBookingFromForm(r *http.Request, kind, direction string) (store.LedgerBookingInput, string) {
	b := models.LedgerBooking{Version: 1, Kind: kind, TaskLabel: trimmed(r, "task_label"), Note: trimmed(r, "note")}
	in := store.LedgerBookingInput{YearID: formInt64(r, "year_id"), NeighborID: formInt64(r, "neighbor_id"), Incoming: direction == "in", IdempotencyKey: trimmed(r, "idempotency_key")}
	var err error
	in.Date, err = time.Parse("2006-01-02", trimmed(r, "entry_date"))
	if err != nil {
		return in, "Bitte ein gültiges Datum angeben."
	}
	if b.TaskLabel == "" {
		return in, "Bitte eine Tätigkeit oder Beschreibung angeben."
	}
	for _, field := range []struct {
		name, value string
		limit       int
	}{{"Tätigkeit", b.TaskLabel, maxNameLen}, {"Notiz", b.Note, maxNoteLen}, {"Idempotency-Key", in.IdempotencyKey, maxNameLen}} {
		if msg := lenError(field.name, field.value, field.limit); msg != "" {
			return in, msg
		}
	}
	var ok bool
	switch kind {
	case "fixed":
		b.Unit, b.Quantity = "Pauschale", decimal.NewFromInt(1)
		b.UnitPrice, ok = positiveBookingDecimal(r, "amount")
	case "quantity":
		b.Unit = trimmed(r, "unit")
		if b.Unit == "__custom" {
			b.Unit = trimmed(r, "unit_custom")
		}
		if b.Unit == "" || b.Unit == "h" || lenError("Einheit", b.Unit, 16) != "" {
			return in, "Bitte eine gültige Mengeneinheit angeben."
		}
		b.Quantity, ok = positiveBookingDecimal(r, "quantity")
		if !ok {
			return in, "Menge muss größer als 0 sein (höchstens vier Nachkommastellen)."
		}
		b.UnitPrice, ok = positiveBookingDecimal(r, "unit_price")
	case "equipment", "labor":
		b.Unit = "h"
		b.Quantity, ok = positiveBookingDecimal(r, "hours")
		if !ok {
			return in, "Stunden müssen größer als 0 sein (höchstens vier Nachkommastellen)."
		}
		if kind == "labor" {
			b.Unit = models.UnitMannstunde
			b.PartnerPerson = trimmed(r, "partner_person")
			b.UnitPrice, ok = positiveBookingDecimal(r, "partner_person_rate")
			if b.PartnerPerson == "" {
				return in, "Bitte die ausführende Person angeben."
			}
		} else {
			b.PartnerLabel = trimmed(r, "partner_label")
			b.UnitPrice, ok = positiveBookingDecimal(r, "partner_rate")
			if b.PartnerLabel == "" {
				return in, "Bitte Traktor, Gespann oder Gefährt des Nachbarn beschreiben."
			}
			b.PartnerPerson = trimmed(r, "partner_person")
			if b.PartnerPerson == "" && (trimmed(r, "partner_person_rate") != "" || trimmed(r, "partner_person_hours") != "") {
				return in, "Bitte die zusätzliche Person angeben oder ihre Stunden und ihren Satz leeren."
			}
			if b.PartnerPerson != "" {
				b.PersonHours = b.Quantity
				if trimmed(r, "partner_person_hours") != "" {
					var valid bool
					b.PersonHours, valid = positiveBookingDecimal(r, "partner_person_hours")
					if !valid {
						return in, "Bitte gültige zusätzliche Mannstunden angeben."
					}
				}
				var valid bool
				b.PersonRate, valid = positiveBookingDecimal(r, "partner_person_rate")
				if !valid {
					return in, "Bitte den vereinbarten Stundensatz der zusätzlichen Person angeben."
				}
			}
		}
	default:
		return in, "Diese Buchungsart kann nicht als Verrechnung gespeichert werden."
	}
	if !ok {
		return in, "Bitte einen positiven Betrag oder Preis angeben (höchstens vier Nachkommastellen)."
	}
	if lenError("Fahrzeug", b.PartnerLabel, maxNameLen) != "" || lenError("Person", b.PartnerPerson, maxNameLen) != "" {
		return in, "Fahrzeug und Person dürfen höchstens 100 Zeichen lang sein."
	}
	if !b.Total().IsPositive() || b.Total().GreaterThanOrEqual(decimal.NewFromInt(10_000_000_000)) {
		return in, "Der Gesamtbetrag liegt außerhalb des zulässigen Bereichs."
	}
	in.Booking = b
	return in, ""
}

// unifiedRequestFingerprint binds new outgoing retry keys to the active form
// inputs, excluding CSRF and derived pricing. Identical retries remain valid
// after catalog changes, while changing direction/type/helper cannot double-book.
func unifiedRequestFingerprint(r *http.Request) string {
	if trimmed(r, "booking_kind") == "" {
		return ""
	}
	kind, direction, _ := unifiedBookingSelection(r)
	fields := []string{"booking_kind", "booking_direction", "neighbor_id", "year_id", "entry_date", "task_label", "note"}
	if trimmed(r, "booking_form_version") == "2" {
		fields = append(fields, "booking_form_version", "person_row_id", "person_id", "person_name", "person_hours", "person_rate", "person_state", "copy_mode", "copy_people")
		if kind == "fixed" {
			fields = append(fields, "amount")
		}
		if kind == "equipment" && trimmed(r, "mode") == "free" {
			fields = append(fields, "partner_label", "partner_rate")
		}
	}
	switch kind {
	case "equipment":
		fields = append(fields, "hours", "mode", "person_id")
		if formInt64(r, "person_id") != 0 {
			fields = append(fields, "person_hours", "person_rate")
		}
		if trimmed(r, "mode") == "manual" {
			fields = append(fields, "tractor_id", "load_level_id", "machine_ids")
		} else {
			fields = append(fields, "gespann_id")
		}
	case "labor":
		fields = append(fields, "hours", "person_id", "person_rate")
	case "quantity":
		fields = append(fields, "unit", "quantity", "unit_price")
		if trimmed(r, "unit") == "__custom" {
			fields = append(fields, "unit_custom")
		}
	}
	values := url.Values{}
	for _, field := range fields {
		for _, value := range r.Form[field] {
			values.Add(field, strings.TrimSpace(value))
		}
	}
	values.Set("booking_kind", kind)
	values.Set("booking_direction", direction)
	sort.Strings(values["machine_ids"])
	sum := sha256.Sum256([]byte(values.Encode()))
	return hex.EncodeToString(sum[:])
}

// rejectUnifiedBooking gives offline capture a recoverable status instead of a
// misleading success redirect, while preserving the interactive flash workflow.
func (s *Server) rejectUnifiedBooking(w http.ResponseWriter, r *http.Request, msg string) {
	if r.Header.Get("X-Offline-Replay") == "1" {
		http.Error(w, msg, http.StatusUnprocessableEntity)
		return
	}
	s.setFlash(w, r, "error", msg)
	redirect(w, r, neighborURL(formInt64(r, "neighbor_id"), formInt64(r, "year_id")))
}

// unifiedBookingError translates account/replay guards without treating a real
// database outage as a permanently invalid offline booking.
func (s *Server) unifiedBookingError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrBookingKindLocked):
		s.rejectUnifiedBooking(w, r, "Buchungsart und Verrechnungsrichtung bleiben beim Bearbeiten erhalten. Bitte bei Bedarf stornieren und neu erfassen.")
	case errors.Is(err, store.ErrIdempotencyConflict):
		s.rejectUnifiedBooking(w, r, "Diese Buchungskennung wurde bereits für eine andere Buchung verwendet. Bitte als neue Buchung erfassen.")
	case errors.Is(err, store.ErrInvoiceLocked):
		s.rejectUnifiedBooking(w, r, "Die Rechnung ist festgeschrieben. Buchungen und Verrechnungen sind gesperrt.")
	case errors.Is(err, store.ErrYearCompleted):
		s.rejectUnifiedBooking(w, r, "Das Abrechnungsjahr ist abgeschlossen.")
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNeighborAnonymized):
		s.rejectUnifiedBooking(w, r, "Dieser Nachbar ist für das Abrechnungsjahr nicht verfügbar.")
	default:
		s.serverError(w, r.URL.Path, err)
	}
}

// handleUnifiedLedgerCreate dispatches only counterclaims and fixed positions.
// It returns false for ordinary outgoing services, keeping their established path.
func (s *Server) handleUnifiedLedgerCreate(w http.ResponseWriter, r *http.Request) bool {
	kind, direction, msg := unifiedBookingSelection(r)
	if msg != "" {
		s.rejectUnifiedBooking(w, r, msg)
		return true
	}
	if direction == "out" && kind != "fixed" {
		return false
	}
	in, msg := ledgerBookingFromForm(r, kind, direction)
	if msg != "" {
		s.rejectUnifiedBooking(w, r, msg)
		return true
	}
	id, err := s.store.CreateLedgerBooking(r.Context(), in)
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return true
	}
	if r.Header.Get("X-Offline-Replay") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	msg = "Verrechnung gespeichert."
	if id == 0 {
		msg = "Verrechnung war bereits erfasst."
	}
	s.setFlash(w, r, "success", msg)
	redirect(w, r, neighborURL(in.NeighborID, in.YearID))
	return true
}

// resolveLaborFromForm prices own labor from the selected person, with an
// explicit agreed-rate override. It retains attribution for edits and copies.
func (s *Server) resolveLaborFromForm(r *http.Request) (*models.Entry, string, error) {
	person, err := s.store.GetPerson(r.Context(), formInt64(r, "person_id"))
	if errors.Is(err, store.ErrNotFound) {
		return nil, "Bitte eine verfügbare Person wählen.", nil
	}
	if err != nil {
		return nil, "", err
	}
	hours, ok := positiveBookingDecimal(r, "hours")
	if !ok {
		return nil, "Bitte gültige Mannstunden größer 0 angeben.", nil
	}
	rate := person.HourlyRate
	if trimmed(r, "person_rate") != "" {
		rate, ok = positiveBookingDecimal(r, "person_rate")
		if !ok {
			return nil, "Bitte einen gültigen Stundensatz angeben.", nil
		}
	}
	if !rate.IsPositive() {
		return nil, "Für diese Person ist kein Stundensatz hinterlegt — bitte einen Satz angeben.", nil
	}
	date, err := time.Parse("2006-01-02", trimmed(r, "entry_date"))
	if err != nil {
		return nil, "Bitte ein gültiges Datum angeben.", nil
	}
	task, note := trimmed(r, "task_label"), trimmed(r, "note")
	if task == "" {
		task = "Mannstunden " + person.Name
	}
	if msg := lenError("Tätigkeit", task, maxNameLen); msg != "" {
		return nil, msg, nil
	}
	if msg := lenError("Notiz", note, maxNoteLen); msg != "" {
		return nil, msg, nil
	}
	cost := hours.Mul(rate).Round(2)
	if !cost.IsPositive() || cost.GreaterThanOrEqual(decimal.NewFromInt(10_000_000_000)) {
		return nil, "Der Gesamtbetrag liegt außerhalb des zulässigen Bereichs.", nil
	}
	return &models.Entry{Date: date, TaskLabel: task, Note: note, Unit: models.UnitMannstunde, Quantity: hours,
		UnitPrice: rate, Cost: cost, PersonID: &person.ID}, "", nil
}

// resolveUnifiedEntryFromForm retains transient lookup failures for offline retry
// while the historical machine/quantity resolver keeps its established contract.
func (s *Server) resolveUnifiedEntryFromForm(r *http.Request) (*models.Entry, []int64, string, error) {
	if trimmed(r, "booking_kind") == "labor" {
		entry, msg, err := s.resolveLaborFromForm(r)
		return entry, nil, msg, err
	}
	if trimmed(r, "booking_kind") != "" {
		if _, err := time.Parse("2006-01-02", trimmed(r, "entry_date")); err != nil {
			return nil, nil, "Bitte ein gültiges Datum angeben.", nil
		}
	}
	entry, ids, msg := s.resolveEntryFromForm(r)
	return entry, ids, msg, nil
}
