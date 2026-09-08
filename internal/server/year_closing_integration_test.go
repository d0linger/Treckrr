package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The pre-close checklist and the closing effect (Ausbaukarte 59/60): what the
// review reports before closing, what a closed year refuses afterwards, and
// that reopening costs a recorded reason.
func TestYearClosingIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	closingURL := fmt.Sprintf("/years/%d/abschluss", yid)

	// A booking with no invoice: the first check must name the neighbor.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-07-01"},
		"hours": {"2"}, "unit": {"h"},
	})
	page := e.get(closingURL)
	if !strings.Contains(page, "Buchungen ohne festgeschriebene Rechnung") {
		t.Fatalf("checklist page did not render")
	}
	checks, err := e.st.YearClosingChecks(e.ctx, yid)
	if err != nil {
		t.Fatalf("checks: %v", err)
	}
	byKey := map[string]int{}
	for _, c := range checks {
		byKey[c.Key] = c.Count
	}
	if byKey["uninvoiced"] != 1 {
		t.Errorf("uninvoiced = %d, want 1", byKey["uninvoiced"])
	}
	if byKey["unpaid"] != 0 {
		t.Errorf("unpaid = %d, want 0 (no invoice yet)", byKey["unpaid"])
	}

	// Issue: the first check clears, "never sent" and "unpaid" open up.
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	checks, _ = e.st.YearClosingChecks(e.ctx, yid)
	byKey = map[string]int{}
	amounts := map[string]string{}
	for _, c := range checks {
		byKey[c.Key] = c.Count
		amounts[c.Key] = c.Amount.StringFixed(2)
	}
	if byKey["uninvoiced"] != 0 {
		t.Errorf("uninvoiced = %d after issuing, want 0", byKey["uninvoiced"])
	}
	if byKey["unsent"] != 1 || byKey["unpaid"] != 1 {
		t.Errorf("unsent=%d unpaid=%d, want 1/1", byKey["unsent"], byKey["unpaid"])
	}
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("invoice: %v", err)
	}
	if amounts["unpaid"] != iv.Content.Gross.StringFixed(2) {
		t.Errorf("open amount = %s, want the invoice gross %s", amounts["unpaid"], iv.Content.Gross.StringFixed(2))
	}
	// Paying it in full clears the open-amount check.
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {iv.Content.Gross.StringFixed(2)}, "paid_on": {"2026-07-02"},
	})
	checks, _ = e.st.YearClosingChecks(e.ctx, yid)
	for _, c := range checks {
		if c.Key == "unpaid" && c.Count != 0 {
			t.Errorf("unpaid = %d after full payment, want 0", c.Count)
		}
	}

	// Close the year.
	e.post(fmt.Sprintf("/years/%d/status", yid), url.Values{"status": {"completed"}})
	year, err := e.st.GetBillingYear(e.ctx, yid)
	if err != nil || !year.Completed() {
		t.Fatalf("year not completed: %v", err)
	}

	// Abschlusswirkung: every document write is refused now.
	docsBefore, err := e.st.ListInvoiceDocuments(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("documents: %v", err)
	}
	for _, path := range []string{
		fmt.Sprintf("/neighbors/%d/invoice/gutschrift", nid),
		fmt.Sprintf("/neighbors/%d/gutschrift", nid),
		fmt.Sprintf("/neighbors/%d/anzahlung", nid),
		fmt.Sprintf("/neighbors/%d/invoice/storno", nid),
	} {
		body := e.post(path, url.Values{"year_id": {itoa64(yid)}, "amount": {"5"}})
		if !strings.Contains(body, "abgeschlossen") {
			t.Errorf("%s did not report the closed year", path)
		}
	}
	docsAfter, err := e.st.ListInvoiceDocuments(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("documents: %v", err)
	}
	if len(docsAfter) != len(docsBefore) {
		t.Errorf("a closed year still accepted %d new document(s)", len(docsAfter)-len(docsBefore))
	}

	// A payment, by contrast, must still be possible — money keeps arriving.
	paysBefore, _ := e.st.ListPayments(e.ctx, yid, nid)
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"5"}, "paid_on": {"2026-07-03"},
	})
	if paysAfter, _ := e.st.ListPayments(e.ctx, yid, nid); len(paysAfter) != len(paysBefore)+1 {
		t.Errorf("a closed year must still accept payments (%d -> %d)", len(paysBefore), len(paysAfter))
	}

	// That extra 5 makes it an overpayment, so a credit now exists — but paying
	// it out writes to the ledger and must be refused while the year is closed.
	ledgerBefore, _ := e.st.NeighborLedgerSum(e.ctx, yid, nid)
	if body := e.post(fmt.Sprintf("/neighbors/%d/credit-payout", nid),
		url.Values{"year_id": {itoa64(yid)}}); !strings.Contains(body, "abgeschlossen") {
		t.Errorf("credit payout did not report the closed year")
	}
	if after, _ := e.st.NeighborLedgerSum(e.ctx, yid, nid); !after.Equal(ledgerBefore) {
		t.Errorf("credit payout wrote to the ledger of a closed year (%s -> %s)", ledgerBefore, after)
	}

	// Reopening without a reason is refused; with one it works and is recorded.
	e.post(fmt.Sprintf("/years/%d/status", yid), url.Values{"status": {"in_progress"}})
	if year, _ = e.st.GetBillingYear(e.ctx, yid); !year.Completed() {
		t.Fatalf("the year was reopened without a reason")
	}
	e.post(fmt.Sprintf("/years/%d/status", yid), url.Values{
		"status": {"in_progress"}, "reason": {"Nachbuchung Mähdrescher"},
	})
	if year, _ = e.st.GetBillingYear(e.ctx, yid); year.Completed() {
		t.Fatalf("the year was not reopened despite a reason")
	}
	var detail string
	if err := e.pool.QueryRowContext(e.ctx,
		`SELECT detail FROM audit_log WHERE action='reopen' AND entity='year' AND entity_id=$1 ORDER BY id DESC LIMIT 1`,
		itoa64(yid)).Scan(&detail); err != nil { // entity_id is TEXT
		t.Fatalf("no reopen audit entry: %v", err)
	}
	if !strings.Contains(detail, "Nachbuchung Mähdrescher") {
		t.Errorf("reopen audit does not carry the reason: %q", detail)
	}

	// And documents work again.
	e.post(fmt.Sprintf("/neighbors/%d/gutschrift", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"5"}, "note": {"nach Wiederöffnung"},
	})
	if docs, _ := e.st.ListInvoiceDocuments(e.ctx, yid, nid); len(docs) != len(docsBefore)+1 {
		t.Errorf("after reopening a credit note must be possible again (%d -> %d)", len(docsBefore), len(docs))
	}
}
