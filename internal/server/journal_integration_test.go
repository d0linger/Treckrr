package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The Rechnungsjournal (Ausbaukarte 47/50/52/55) through the real handlers:
// chosen issue date, storno visibility, signed totals, CSV and ZIP export, and
// the Kleinunternehmer warning at issue time.
func TestJournalAndArchiveIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// Invoice 1: booked and issued with a chosen (yesterday) Rechnungsdatum.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-20"},
		"hours": {"2"}, "unit": {"h"},
	})
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{
		"year_id": {itoa64(yid)}, "issued_on": {yesterday},
	})
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("invoice: %v", err)
	}
	if iv.IssuedOn.Format("2006-01-02") != yesterday {
		t.Errorf("issued_on = %s, want the chosen %s", iv.IssuedOn.Format("2006-01-02"), yesterday)
	}

	// Invoice 2 on a second neighbor, then storno — the journal must show the
	// canceled original AND the storno, with the pair netting out of the total.
	n2, err := e.st.CreateNeighbor(e.ctx, "Zweiter "+e.uname, "")
	if err != nil {
		t.Fatalf("neighbor 2: %v", err)
	}
	if err := e.st.UpdateNeighbor(e.ctx, n2, "Zweiter "+e.uname, "", "Feldweg 2, 4710 Testdorf", "", "", "", nil); err != nil {
		t.Fatalf("neighbor 2 address: %v", err)
	}
	if err := e.st.AddNeighborToYear(e.ctx, yid, n2); err != nil {
		t.Fatalf("neighbor 2 year: %v", err)
	}
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(n2)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-21"},
		"hours": {"1"}, "unit": {"h"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/invoice", n2), url.Values{"year_id": {itoa64(yid)}})
	iv2, err := e.st.GetInvoice(e.ctx, yid, n2)
	if err != nil {
		t.Fatalf("invoice 2: %v", err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/invoice/storno", n2), url.Values{
		"year_id": {itoa64(yid)}, "reason": {"Testfall"},
	})

	page := e.get(fmt.Sprintf("/rechnungsjournal?year=%d", yid))
	for _, want := range []string{iv.Number, iv2.Number, iv2.Number + "-S", "storniert", "Summe"} {
		if !strings.Contains(page, want) {
			t.Errorf("journal page is missing %q", want)
		}
	}
	// Signed total = invoice 1 alone (92,00): the canceled pair nets to zero.
	if !strings.Contains(page, "92,00") {
		t.Errorf("signed total does not show invoice 1's 92,00")
	}

	// CSV: dialect header and both documents.
	csvBody := e.get(fmt.Sprintf("/rechnungsjournal/export.csv?year=%d", yid))
	if !strings.Contains(csvBody, "Nummer;Datum;Art") || !strings.Contains(csvBody, iv.Number) || !strings.Contains(csvBody, iv2.Number+"-S") {
		t.Errorf("CSV export incomplete")
	}

	// ZIP: a real archive with content.
	zipBody := e.get(fmt.Sprintf("/rechnungsjournal/archiv.zip?year=%d", yid))
	if !strings.HasPrefix(zipBody, "PK") || len(zipBody) < 2000 {
		t.Errorf("ZIP archive missing or implausibly small (%d bytes)", len(zipBody))
	}

	// Kleinunternehmer ceiling (Nr. 55): with mode + tiny limit set, issuing the
	// next invoice must warn — and the journal shows the meter. Restore the
	// company afterwards so later tests see the usual pauschal setup.
	setCompany := func(mode, limit string) {
		e.post("/admin/company", url.Values{
			"name": {"IT-Betrieb"}, "address": {"Hofstraße 2, 4710 Testdorf"},
			"tax_id": {"ATU00000000"}, "tax_mode": {mode}, "vat_rate": {"13"},
			"payment_term_days": {"14"}, "small_business_limit": {limit},
		})
	}
	setCompany("kleinunternehmer", "50")
	defer setCompany("pauschal", "0")
	n3, err := e.st.CreateNeighbor(e.ctx, "Dritter "+e.uname, "")
	if err != nil {
		t.Fatalf("neighbor 3: %v", err)
	}
	if err := e.st.UpdateNeighbor(e.ctx, n3, "Dritter "+e.uname, "", "Feldweg 3, 4710 Testdorf", "", "", "", nil); err != nil {
		t.Fatalf("neighbor 3 address: %v", err)
	}
	if err := e.st.AddNeighborToYear(e.ctx, yid, n3); err != nil {
		t.Fatalf("neighbor 3 year: %v", err)
	}
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(n3)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-22"},
		"hours": {"2"}, "unit": {"h"},
	})
	flashPage := e.post(fmt.Sprintf("/neighbors/%d/invoice", n3), url.Values{"year_id": {itoa64(yid)}})
	if !strings.Contains(flashPage, "Kleinunternehmergrenze") {
		t.Errorf("issue flash carries no Kleinunternehmer warning although 92 > 50")
	}
	if page := e.get(fmt.Sprintf("/rechnungsjournal?year=%d", yid)); !strings.Contains(page, "Kleinunternehmergrenze") {
		t.Errorf("journal page shows no KU meter in kleinunternehmer mode")
	}
}
