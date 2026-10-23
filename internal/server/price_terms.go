package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// catalogMachineHours reports whether a booking bills catalog machine hours:
// a tractor, rig or machines, by the hour, not attributed to a person.
func catalogMachineHours(entry *models.Entry, machineIDs []int64) bool {
	if entry == nil || entry.PersonID != nil || (entry.Unit != "h" && entry.Unit != "") {
		return false
	}
	return entry.GespannID != nil || entry.TractorID != nil || len(machineIDs) > 0
}

// applyPriceTerm discounts the catalog machine rate of an own booking by the
// neighbor's standing term and snapshots it; without a term (or for any other
// booking) the snapshot is cleared. The cost follows the new rate.
func applyPriceTerm(entry *models.Entry, machineIDs []int64, term *models.PriceTerm) {
	if entry == nil {
		return
	}
	entry.PriceTermLabel, entry.PriceTermPct, entry.PriceTermPerH = "", decimal.Zero, decimal.Zero
	if term == nil || !catalogMachineHours(entry, machineIDs) {
		return
	}
	list := entry.HourlyRate
	rate, perH := term.Discounted(list)
	entry.PriceTermLabel, entry.PriceTermPct, entry.PriceTermPerH = term.Label, term.DiscountPct, perH
	entry.HourlyRate = rate
	if entry.UnitPrice.Equal(list) {
		entry.UnitPrice = rate
	}
	entry.Cost = calc.Cost(entry.Hours, rate)
}

// applyNeighborPriceTerm looks up the neighbor's term effective on the
// booking date and applies it.
func (s *Server) applyNeighborPriceTerm(ctx context.Context, entry *models.Entry, machineIDs []int64, neighborID int64) error {
	term, err := s.store.PriceTermAt(ctx, neighborID, entry.Date)
	if err != nil {
		return err
	}
	applyPriceTerm(entry, machineIDs, term)
	return nil
}

// priceTermJSON is the booking preview's copy of a term.
type priceTermJSON struct {
	From  string  `json:"from"`
	To    string  `json:"to,omitempty"`
	Pct   float64 `json:"pct"`
	Label string  `json:"label"`
}

// priceTermsAttr serializes a neighbor's terms for the booking form preview,
// which picks the term effective on the chosen date like the server does.
func priceTermsAttr(terms []models.PriceTerm) string {
	if len(terms) == 0 {
		return ""
	}
	out := make([]priceTermJSON, 0, len(terms))
	for _, t := range terms {
		item := priceTermJSON{From: t.ValidFrom.Format("2006-01-02"), Pct: t.DiscountPct.InexactFloat64(), Label: t.Label}
		if t.ValidTo != nil {
			item.To = t.ValidTo.Format("2006-01-02")
		}
		out = append(out, item)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// priceTermsData adds a neighbor's terms to a page that shows the price-term
// card or the booking form, including the term effective today.
func (s *Server) priceTermsData(ctx context.Context, data pageData, neighborID int64) error {
	terms, err := s.store.ListPriceTerms(ctx, neighborID)
	if err != nil {
		return err
	}
	data["PriceTerms"] = terms
	data["PriceTermsJSON"] = priceTermsAttr(terms)
	data["PriceTermToday"] = models.EffectivePriceTerm(terms, time.Now())
	return nil
}

// priceTermSummary is the audit wording of a term.
func priceTermSummary(t models.PriceTerm) string {
	period := "ab " + web.Date(t.ValidFrom)
	if t.ValidTo != nil {
		period += " bis " + web.Date(*t.ValidTo)
	}
	return t.Label + " · " + web.Num(t.DiscountPct) + " % auf eigene Maschinenstunden · " + period
}

// handlePriceTermCreate adds a standing price term to a neighbor. It applies to
// own catalog machine hours booked or edited from now on; open bookings change
// only through the reviewed recalc.
func (s *Server) handlePriceTermCreate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	back := neighborURL(id, formInt64(r, "year")) + "#konditionen"
	if neighbor.Anonymized {
		s.setFlash(w, r, "error", "Für einen anonymisierten Nachbarn können keine Preisvereinbarungen angelegt werden.")
		redirect(w, r, back)
		return
	}
	term, msg := priceTermFromForm(r)
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, back)
		return
	}
	term.NeighborID = id
	if _, err := s.store.CreatePriceTerm(r.Context(), term); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "price_term", "neighbor", id, neighbor.Name+" · "+priceTermSummary(term))
	s.setFlash(w, r, "success", "Preisvereinbarung gespeichert. Sie gilt für neue und bearbeitete Maschinenbuchungen; offene Buchungen über „Neu berechnen“.")
	redirect(w, r, back)
}

// priceTermFromForm validates the term fields; the message is the German
// flash for an invalid entry.
func priceTermFromForm(r *http.Request) (models.PriceTerm, string) {
	var t models.PriceTerm
	t.Label = strings.TrimSpace(r.FormValue("label"))
	if t.Label == "" || lenError("Bezeichnung", t.Label, maxNameLen) != "" {
		return t, "Bitte eine Bezeichnung mit höchstens 100 Zeichen angeben, z. B. „Familienrabatt“."
	}
	pct, ok := parseGermanDecimalOK(r.FormValue("discount_pct"))
	if !ok || !pct.IsPositive() || pct.GreaterThanOrEqual(decimal.NewFromInt(100)) || !pct.Equal(pct.Round(2)) {
		return t, "Bitte einen Rabatt über 0 und unter 100 % mit höchstens zwei Nachkommastellen angeben."
	}
	t.DiscountPct = pct
	from, err := time.Parse("2006-01-02", strings.TrimSpace(r.FormValue("valid_from")))
	if err != nil {
		return t, "Bitte ein gültiges „Gültig ab“-Datum angeben."
	}
	t.ValidFrom = from
	if raw := strings.TrimSpace(r.FormValue("valid_to")); raw != "" {
		to, err := time.Parse("2006-01-02", raw)
		if err != nil || to.Before(from) {
			return t, "Das „Gültig bis“-Datum muss gültig sein und darf nicht vor „Gültig ab“ liegen."
		}
		t.ValidTo = &to
	}
	return t, ""
}

// handlePriceTermDelete removes a term. Bookings keep the term they were
// priced with until they are edited or recalculated.
func (s *Server) handlePriceTermDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := neighborURL(id, formInt64(r, "year")) + "#konditionen"
	term, err := s.store.DeletePriceTerm(r.Context(), id, formInt64(r, "term_id"))
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "info", "Die Preisvereinbarung war bereits gelöscht.")
		redirect(w, r, back)
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "price_term_delete", "neighbor", id, s.neighborName(r, id)+" · "+priceTermSummary(term))
	s.setFlash(w, r, "success", "Preisvereinbarung gelöscht. Bereits gebuchte Leistungen behalten ihren Preis bis zur Neuberechnung.")
	redirect(w, r, back)
}
