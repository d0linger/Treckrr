package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The Art. 15 export must contain everything Treckrr holds about a person, and
// anonymisation must actually erase the free text and photos — with a typed
// confirmation in front of it (Ausbaukarte 86/87/88).
func TestDSGVOExportAndAnonymizeIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// A booking with a personal note and task, a photo, a payment, a ledger
	// posting, an installment, a recorded send and a public link.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-01"},
		"hours": {"2"}, "unit": {"h"},
		"task_label": {"Mähen bei Familie Testperson"}, "note": {"Handy 0664 12345"},
	})
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	eid := entries[0].ID
	e.postFiles(fmt.Sprintf("/entries/%d/photos", eid), "photo", map[string][]byte{
		"wiegeschein.png": pngBytes(t),
	})
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"20"}, "paid_on": {"2026-05-02"},
		"method": {"bar"}, "note": {"bar an der Hoftür"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/ledger", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"15"}, "direction": {"credit"},
		"description": {"Gegenleistung Heuernte"}, "posting_date": {"2026-05-03"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/installments", nid), url.Values{
		"year_id": {itoa64(yid)}, "due_on": {"2026-06-01"}, "amount": {"25"}, "note": {"1. Rate"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/beleg/mark-sent?year=%d", nid, yid), url.Values{})
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	e.post(fmt.Sprintf("/neighbors/%d/beleg/share?year=%d", nid, yid), url.Values{"days": {"14"}})

	// The export must carry all of it.
	body := e.get(fmt.Sprintf("/neighbors/%d/dsgvo-export.json", nid))
	var export struct {
		Subject struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"subject"`
		BillingYears []struct {
			Payments     []map[string]any `json:"payments"`
			Ledger       []map[string]any `json:"ledger"`
			Photos       []map[string]any `json:"photos"`
			Sends        []map[string]any `json:"sends"`
			ShareLinks   []map[string]any `json:"share_links"`
			Installments []map[string]any `json:"installments"`
		} `json:"billing_years"`
	}
	if err := json.Unmarshal([]byte(body), &export); err != nil {
		t.Fatalf("export is not valid JSON: %v", err)
	}
	if len(export.BillingYears) != 1 {
		t.Fatalf("export covers %d years, want 1", len(export.BillingYears))
	}
	y := export.BillingYears[0]
	for name, n := range map[string]int{
		"payments": len(y.Payments), "ledger": len(y.Ledger), "photos": len(y.Photos),
		"sends": len(y.Sends), "share_links": len(y.ShareLinks), "installments": len(y.Installments),
	} {
		if n == 0 {
			t.Errorf("the Auskunft contains no %s", name)
		}
	}
	if !strings.Contains(body, "bar an der Hoftür") || !strings.Contains(body, "Gegenleistung Heuernte") {
		t.Errorf("free-text records are missing from the export")
	}

	// Anonymisation without the typed word must do nothing.
	e.post(fmt.Sprintf("/neighbors/%d/anonymize", nid), url.Values{})
	if n, _ := e.st.GetNeighbor(e.ctx, nid); n.Anonymized {
		t.Fatalf("the neighbor was anonymised without the typed confirmation")
	}

	// With the word: master data, free text and photos are gone.
	e.post(fmt.Sprintf("/neighbors/%d/anonymize", nid), url.Values{"confirm": {"ANONYMISIEREN"}})
	n, err := e.st.GetNeighbor(e.ctx, nid)
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if !n.Anonymized || strings.Contains(n.Name, e.uname) {
		t.Fatalf("neighbor not anonymised: %v / %q", n.Anonymized, n.Name)
	}
	after, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(after) != 1 {
		t.Fatalf("entries after anonymise: %v (n=%d)", err, len(after))
	}
	if after[0].Note != "" || after[0].TaskLabel != "" {
		t.Errorf("free text survived anonymisation: note=%q task=%q", after[0].Note, after[0].TaskLabel)
	}
	if after[0].Cost.StringFixed(2) != "92.00" {
		t.Errorf("the amount was destroyed by anonymisation: %s", after[0].Cost)
	}
	if counts, err := e.st.PhotoCounts(e.ctx, yid, nid); err != nil || counts[eid] != 0 {
		t.Errorf("photos survived anonymisation: %d (%v)", counts[eid], err)
	}
	if pays, _ := e.st.ListPayments(e.ctx, yid, nid); len(pays) != 1 || pays[0].Note != "" {
		t.Errorf("payment note survived anonymisation")
	}
	if ledger, _ := e.st.ListNeighborLedger(e.ctx, yid, nid); len(ledger) != 1 || ledger[0].Description != "" {
		t.Errorf("ledger description survived anonymisation")
	}
	if shares, _ := e.st.ListBelegShares(e.ctx, nid, yid); len(shares) != 0 {
		t.Errorf("a public link survived anonymisation — it would still serve the document")
	}
	// The frozen invoice is deliberately kept: it is the tax record.
	if iv, err := e.st.GetInvoice(e.ctx, yid, nid); err != nil || iv.Content == nil {
		t.Errorf("the frozen invoice must survive anonymisation: %v", err)
	}
}
