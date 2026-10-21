//go:build integration

package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestPriceTermsIntegration pins a neighbor's standing price term end to end:
// own catalog machine hours booked in its validity range carry the discount
// (older form, unified form, quick entry), incoming work and bookings outside
// the range keep the catalog rate, an open booking from before the term changes
// only through the reviewed recalc, and the Beleg and the frozen invoice line
// name the discount. Deleting the term keeps booked prices.
func TestPriceTermsIntegration(t *testing.T) {
	e := newItEnv(t)
	dec := decimal.RequireFromString
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	termsURL := "/neighbors/" + nid + "/price-terms?year=" + yid
	byDate := func() map[string]models.Entry {
		t.Helper()
		entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]models.Entry{}
		for _, en := range entries {
			out[en.Date.Format("2006-01-02")] = en
		}
		return out
	}
	legacy := func(date string) {
		e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid},
			"gespann_id": {itoa64(e.gespannID)}, "entry_date": {date}, "hours": {"1"}, "unit": {"h"}})
	}
	unified := func(date, direction string) {
		e.post("/entries", url.Values{"booking_form_version": {"2"}, "booking_kind": {"equipment"}, "booking_direction": {direction},
			"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {date}, "mode": {"gespann"},
			"gespann_id": {itoa64(e.gespannID)}, "hours": {"1"}})
	}

	legacy("2026-04-03") // open booking from before the term

	e.post(termsURL, url.Values{"label": {"Familienrabatt"}, "discount_pct": {"100"}, "valid_from": {"2026-04-01"}})
	if terms, _ := e.st.ListPriceTerms(e.ctx, e.neighborID); len(terms) != 0 {
		t.Fatalf("a 100 %% discount was stored: %+v", terms)
	}
	e.post(termsURL, url.Values{"label": {"Familienrabatt"}, "discount_pct": {"10"},
		"valid_from": {"2026-04-01"}, "valid_to": {"2026-04-30"}})
	terms, err := e.st.ListPriceTerms(e.ctx, e.neighborID)
	if err != nil || len(terms) != 1 {
		t.Fatalf("term not stored: %+v, %v", terms, err)
	}
	if page := e.get(neighborURL(e.neighborID, e.yearID64)); !strings.Contains(page, "10 % Familienrabatt") {
		t.Error("neighbor page does not list the term")
	}
	if form := e.get(neighborBookingURL(e.neighborID, e.yearID64)); !strings.Contains(form, "data-price-terms=") {
		t.Error("booking form does not pass the term to the preview")
	}

	legacy("2026-04-10")
	unified("2026-04-11", "out")
	unified("2026-04-12", "in")
	e.post("/entries/quick", url.Values{"year_id": {yid}, "neighbor_id": {nid},
		"q_date": {"2026-04-13"}, "q_gespann": {itoa64(e.gespannID)}, "q_hours": {"1"}})
	legacy("2026-05-02") // after the term ended

	got := byDate()
	for date, want := range map[string]string{
		"2026-04-03": "46", // booked before the term: unchanged until recalc
		"2026-04-10": "41.4",
		"2026-04-11": "41.4",
		"2026-04-13": "41.4",
		"2026-05-02": "46",
	} {
		en, ok := got[date]
		if !ok {
			t.Errorf("%s: booking missing", date)
			continue
		}
		if !en.HourlyRate.Equal(dec(want)) || !en.Cost.Equal(en.Hours.Mul(en.HourlyRate).Round(2)) {
			t.Errorf("%s: rate %s, cost %s; want rate %s", date, en.HourlyRate, en.Cost, want)
		}
		discounted := want == "41.4"
		if discounted != (en.PriceTermLabel == "Familienrabatt" && en.PriceTermPct.Equal(dec("10")) && en.PriceTermPerH.Equal(dec("4.6"))) {
			t.Errorf("%s: term snapshot %q %s%% −%s", date, en.PriceTermLabel, en.PriceTermPct, en.PriceTermPerH)
		}
	}
	var incoming decimal.Decimal
	if err := e.pool.QueryRowContext(e.ctx, `SELECT amount FROM neighbor_ledger
		WHERE billing_year_id=$1 AND neighbor_id=$2 AND posting_date='2026-04-12' AND NOT voided`, e.yearID64, e.neighborID).Scan(&incoming); err != nil || !incoming.Equal(dec("-46")) {
		t.Errorf("incoming work = %s (%v), want the undiscounted -46.00", incoming, err)
	}

	preview := e.get("/neighbors/" + nid + "/recalc?year=" + yid)
	if !strings.Contains(preview, "abzgl. 10 % Familienrabatt") {
		t.Error("recalc preview does not offer the term for the older open booking")
	}
	e.post("/neighbors/"+nid+"/recalc", url.Values{"year_id": {yid}})
	if old := byDate()["2026-04-03"]; !old.HourlyRate.Equal(dec("41.4")) || old.PriceTermLabel != "Familienrabatt" {
		t.Errorf("recalc did not apply the term: rate %s, term %q", old.HourlyRate, old.PriceTermLabel)
	}
	if after := byDate()["2026-05-02"]; !after.HourlyRate.Equal(dec("46")) {
		t.Errorf("recalc discounted a booking outside the term: %s", after.HourlyRate)
	}

	if beleg := e.get("/neighbors/" + nid + "/beleg?year=" + yid); !strings.Contains(beleg, "abzgl. 10 % Familienrabatt") {
		t.Error("Beleg does not name the discount")
	}
	e.postIssue(e.neighborID, url.Values{"year_id": {yid}})
	invoice, err := e.st.GetInvoice(e.ctx, e.yearID64, e.neighborID)
	if err != nil || invoice.Content == nil {
		t.Fatalf("invoice not issued: %v", err)
	}
	named := 0
	for _, line := range invoice.Content.Lines {
		if strings.HasSuffix(line.Label, "(abzgl. 10 % Familienrabatt)") {
			named++
		}
	}
	if named != 4 {
		t.Errorf("%d frozen invoice lines name the discount, want 4: %+v", named, invoice.Content.Lines)
	}

	e.post("/neighbors/"+nid+"/price-terms/delete?year="+yid, url.Values{"term_id": {itoa64(terms[0].ID)}})
	if left, _ := e.st.ListPriceTerms(e.ctx, e.neighborID); len(left) != 0 {
		t.Errorf("term not deleted: %+v", left)
	}
	if kept := byDate()["2026-04-10"]; !kept.HourlyRate.Equal(dec("41.4")) {
		t.Errorf("deleting the term changed a booked price to %s", kept.HourlyRate)
	}
}
