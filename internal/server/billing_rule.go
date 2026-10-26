package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// applyBillingRule bills the catalog machine hours of a booking by the basis'
// increment (rounded up) and minimum, recomputing the cost from the frozen
// hourly rate. Person hours, free-text equipment and non-hour units are left
// alone. It returns the measured hours and whether they changed.
func applyBillingRule(entry *models.Entry, machineIDs []int64, base *models.PriceBase) (decimal.Decimal, bool) {
	if base == nil || !base.HasBillingRule() || !catalogMachineHours(entry, machineIDs) {
		return decimal.Zero, false
	}
	measured := entry.Hours
	billed := base.BilledHours(measured)
	if billed.Equal(measured) {
		return measured, false
	}
	if entry.Quantity.Equal(measured) {
		entry.Quantity = billed
	}
	entry.Hours = billed
	entry.Cost = calc.Cost(billed, entry.HourlyRate)
	return measured, true
}

// parseBillingHours reads an optional billing-rule field in hours: empty means
// off (0); otherwise 0 to 24 h with at most three decimal places.
func parseBillingHours(raw string) (decimal.Decimal, bool) {
	if strings.TrimSpace(raw) == "" {
		return decimal.Zero, true
	}
	v, ok := parseGermanDecimalOK(raw)
	if !ok || v.IsNegative() || v.GreaterThan(decimal.NewFromInt(24)) || !v.Equal(v.Round(3)) {
		return decimal.Zero, false
	}
	return v, true
}

// handleBaseBillingRule stores the billing increment and minimum of an unlocked
// basis. They apply to catalog machine hours booked or edited from now on;
// stored bookings keep their hours until someone edits them.
func (s *Server) handleBaseBillingRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	back := "/bases#grundlage-" + strconv.FormatInt(id, 10)
	// Same length guard as the fuel adjustment's amount: an oversized value gets
	// its own message instead of the generic range hint.
	if s.tooLong(w, r, "Abrechnungstakt", r.FormValue("billing_increment_h"), maxDecimalLen) ||
		s.tooLong(w, r, "Mindestzeit", r.FormValue("minimum_h"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	increment, okIncrement := parseBillingHours(r.FormValue("billing_increment_h"))
	minimum, okMinimum := parseBillingHours(r.FormValue("minimum_h"))
	if !okIncrement || !okMinimum {
		s.setFlash(w, r, "error", "Abrechnungstakt und Mindestzeit bitte in Stunden zwischen 0 und 24 mit höchstens drei Nachkommastellen angeben.")
		redirect(w, r, back)
		return
	}
	before, err := s.store.GetBase(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if err := s.store.UpdateBaseBillingRule(r.Context(), id, increment, minimum); errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Die Grundlage ist gesperrt — bitte zuerst entsperren.")
		redirect(w, r, back)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	detail := diffFields(
		fieldChange{"Abrechnungstakt", web.Num(before.BillingIncrementH) + " h", web.Num(increment) + " h"},
		fieldChange{"Mindestzeit", web.Num(before.MinimumH) + " h", web.Num(minimum) + " h"},
	)
	if detail == "" {
		detail = before.Name + ": Abrechnungsregel unverändert"
	} else {
		detail = before.Name + " · " + detail
	}
	s.audit(r, "update", "base", id, detail)
	s.setFlash(w, r, "success", "Abrechnungsregel gespeichert. Sie gilt für neue und bearbeitete Maschinenbuchungen.")
	redirect(w, r, back)
}
