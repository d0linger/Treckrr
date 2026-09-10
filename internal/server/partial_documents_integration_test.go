package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// Abschlag (Anzahlung) and free credit note through the real handlers
// (Ausbaukarte 53/54), including what each one must NOT do: an Abschlag is no
// revenue and does not lock the bookings, and its Storno must not subtract
// revenue that was never counted.
func TestPartialDocumentsIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	belegURL := fmt.Sprintf("/neighbors/%d/beleg?year=%d", nid, yid)

	// 2 h × 46 = 92,00 booked, no invoice yet — the season is running.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-06-01"},
		"hours": {"2"}, "unit": {"h"},
	})

	// Two Abschläge: 30 and 20.
	e.post(fmt.Sprintf("/neighbors/%d/anzahlung", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"30"}, "label": {"1. Abschlag"}, "due_on": {"2026-06-02"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/anzahlung", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"20"}, "label": {"2. Abschlag"}, "due_on": {"2026-06-03"},
	})
	anz, err := e.st.ListAnzahlungen(e.ctx, yid, nid)
	if err != nil || len(anz) != 2 {
		t.Fatalf("Abschläge: %v (n=%d)", err, len(anz))
	}
	if !strings.HasSuffix(anz[0].Number, "-A001") || !strings.HasSuffix(anz[1].Number, "-A002") {
		t.Errorf("Abschlag numbering = %q / %q, want …-A001 / …-A002", anz[0].Number, anz[1].Number)
	}
	if anz[0].Content == nil || anz[0].Content.ShowVAT {
		t.Errorf("an Abschlag must carry a snapshot and must NOT show USt")
	}
	if sum, _ := e.st.AnzahlungSum(e.ctx, yid, nid); sum.StringFixed(2) != "50.00" {
		t.Errorf("Abschlagssumme = %s, want 50.00", sum.StringFixed(2))
	}

	// The bookings must still be editable: an Abschlag is not a Festschreibung.
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); err == nil {
		t.Errorf("an Abschlag must not occupy the invoice slot")
	}
	if page := e.get(belegURL); !strings.Contains(page, "Rechnung ausstellen") {
		t.Errorf("issuing a Rechnung must still be offered while only Abschläge exist")
	}

	// Journal: both Abschläge are listed but carry no revenue.
	page := e.get(fmt.Sprintf("/rechnungsjournal?year=%d", yid))
	if !strings.Contains(page, "Abschlag") || !strings.Contains(page, anz[0].Number) {
		t.Errorf("journal does not list the Abschläge")
	}
	if !strings.Contains(page, "nicht im Umsatz") {
		t.Errorf("journal does not mark the Abschlag as revenue-neutral")
	}

	// Storno of one Abschlag: reversal document, original canceled, sum drops —
	// and the revenue stays zero (the pair must not net to a NEGATIVE total).
	e.post(fmt.Sprintf("/documents/%d/storno", anz[0].ID), url.Values{"reason": {"Tippfehler"}})
	if sum, _ := e.st.AnzahlungSum(e.ctx, yid, nid); sum.StringFixed(2) != "20.00" {
		t.Errorf("Abschlagssumme after storno = %s, want 20.00", sum.StringFixed(2))
	}
	journal, err := e.st.ListInvoiceJournal(e.ctx, yid)
	if err != nil {
		t.Fatalf("journal: %v", err)
	}
	for _, row := range journal {
		if row.CountsForRevenue() {
			t.Errorf("row %s (%s/%s ref=%s) counts as revenue although only Abschläge exist",
				row.Number, row.Kind, row.Status, row.RefKind)
		}
	}

	// Now the Schlussrechnung: the single tax document, at its full amount.
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("Schlussrechnung: %v", err)
	}
	// The invariant, not a magic number: the Abschläge (50, one of them
	// stornoed) contributed NOTHING, so the only revenue is the Schlussrechnung
	// at its own full gross.
	gross := iv.Content.Gross
	journal, _ = e.st.ListInvoiceJournal(e.ctx, yid)
	var revenue string
	for _, row := range journal {
		if row.CountsForRevenue() {
			if revenue != "" {
				t.Errorf("more than one revenue row: %s", row.Number)
			}
			revenue = row.Gross.StringFixed(2)
		}
	}
	if revenue != gross.StringFixed(2) {
		t.Errorf("counted revenue = %q, want exactly the Schlussrechnung's %s (Abschläge must not add)",
			revenue, gross.StringFixed(2))
	}

	// A second Abschlag is refused once the Schlussrechnung exists.
	e.post(fmt.Sprintf("/neighbors/%d/anzahlung", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"5"},
	})
	if again, _ := e.st.ListAnzahlungen(e.ctx, yid, nid); len(again) != 2 {
		t.Errorf("an Abschlag was created although a Schlussrechnung exists (n=%d)", len(again))
	}

	// Free credit note (53): works without an active invoice reference and
	// lowers the payable remaining like an attached one.
	before, err := e.st.InvoiceRemaining(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/gutschrift", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"12"}, "note": {"Kulanz"},
	})
	after, err := e.st.InvoiceRemaining(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("remaining: %v", err)
	}
	if before.Sub(after).StringFixed(2) != "12.00" {
		t.Errorf("free credit note moved the remaining by %s, want 12.00", before.Sub(after).StringFixed(2))
	}
	docs, err := e.st.ListInvoiceDocuments(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("documents: %v", err)
	}
	var free *string
	for i := range docs {
		if docs[i].Kind == "gutschrift" && docs[i].ReferencesInvoiceID == nil {
			free = &docs[i].Number
		}
	}
	if free == nil {
		t.Fatalf("no reference-free credit note was stored")
	}
	if !strings.HasSuffix(*free, "-G001") {
		t.Errorf("free credit note number = %q, want …-G001", *free)
	}
	// It IS revenue-effective (negative), unlike an Abschlag.
	journal, _ = e.st.ListInvoiceJournal(e.ctx, yid)
	total := decimal.Zero
	for _, row := range journal {
		if row.CountsForRevenue() {
			total = total.Add(row.Gross)
		}
	}
	want := gross.Sub(decimal.NewFromInt(12))
	if total.StringFixed(2) != want.StringFixed(2) {
		t.Errorf("signed revenue = %s, want %s (Schlussrechnung − 12 Gutschrift)",
			total.StringFixed(2), want.StringFixed(2))
	}
}
