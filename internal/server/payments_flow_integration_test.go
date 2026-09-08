package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Payments gained a method, an invoice link, an edit path, credit handling and
// the Skonto clause (Ausbaukarte 38-42). One walk through the real handlers.
func TestPaymentFlowIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// Booking (2 h × 46 = 92,00) and a frozen invoice, so payments link to it.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-01"},
		"hours": {"2"}, "unit": {"h"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})

	// Payment with a method: the row must show method AND the linked invoice.
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"40"}, "paid_on": {"2026-05-02"},
		"method": {"bar"}, "note": {"Anzahlung"},
	})
	pays, err := e.st.ListPayments(e.ctx, yid, nid)
	if err != nil || len(pays) != 1 {
		t.Fatalf("payments: %v (n=%d)", err, len(pays))
	}
	p := pays[0]
	if p.Method != "bar" || p.InvoiceID == nil || p.InvoiceNumber == "" {
		t.Fatalf("payment not linked: method=%q invoice=%v/%q", p.Method, p.InvoiceID, p.InvoiceNumber)
	}
	page := e.get(fmt.Sprintf("/neighbors/%d?year=%d", nid, yid))
	if !strings.Contains(page, "bar · Rechnung "+p.InvoiceNumber) {
		t.Errorf("payment row does not show method and invoice number")
	}

	// Edit: 40 -> 45, method Überweisung. The correction path payments never had.
	e.post(fmt.Sprintf("/payments/%d/update", p.ID), url.Values{
		"amount": {"45"}, "paid_on": {"2026-05-03"}, "method": {"überweisung"}, "note": {"korrigiert"},
	})
	got, err := e.st.GetPayment(e.ctx, p.ID)
	if err != nil {
		t.Fatalf("get payment: %v", err)
	}
	if got.Amount.StringFixed(2) != "45.00" || got.Method != "überweisung" || got.Note != "korrigiert" {
		t.Errorf("update lost data: %s / %s / %s", got.Amount, got.Method, got.Note)
	}
	// An unknown method must collapse to "" — the whitelist, not the client, decides.
	e.post(fmt.Sprintf("/payments/%d/update", p.ID), url.Values{
		"amount": {"45"}, "paid_on": {"2026-05-03"}, "method": {"bitcoin"}, "note": {"korrigiert"},
	})
	if got, _ := e.st.GetPayment(e.ctx, p.ID); got.Method != "" {
		t.Errorf("unknown method %q was stored", got.Method)
	}

	// Overpay: 92 owed, 45 paid, pay another 100 -> credit 53. Both credit
	// actions must appear; the payout books a ledger posting that zeroes it.
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"100"}, "paid_on": {"2026-05-04"},
	})
	page = e.get(fmt.Sprintf("/neighbors/%d?year=%d", nid, yid))
	if !strings.Contains(page, "Guthaben (53,00") {
		t.Fatalf("credit actions missing or wrong amount (want Guthaben (53,00 …))")
	}
	e.post(fmt.Sprintf("/neighbors/%d/credit-payout", nid), url.Values{"year_id": {itoa64(yid)}})
	rest, err := e.st.NeighborLedgerSum(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("ledger sum: %v", err)
	}
	if rest.StringFixed(2) != "53.00" {
		t.Errorf("payout posting = %s, want 53.00", rest.StringFixed(2))
	}
	page = e.get(fmt.Sprintf("/neighbors/%d?year=%d", nid, yid))
	if strings.Contains(page, "Guthaben (") {
		t.Errorf("credit actions still shown after payout")
	}
	if !strings.Contains(page, "Guthaben ausbezahlt") {
		t.Errorf("payout posting not visible in the ledger list")
	}

	// A second payout must refuse: the credit is gone.
	e.post(fmt.Sprintf("/neighbors/%d/credit-payout", nid), url.Values{"year_id": {itoa64(yid)}})
	if rest2, _ := e.st.NeighborLedgerSum(e.ctx, yid, nid); rest2.StringFixed(2) != "53.00" {
		t.Errorf("double payout changed the ledger: %s", rest2.StringFixed(2))
	}
}

// The Skonto clause appears on the Beleg exactly while the offer stands: with
// pct+days configured and the deadline (issue date + days) not yet passed.
func TestSkontoClauseOnBelegIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-05"},
		"hours": {"1"}, "unit": {"h"},
	})

	setSkonto := func(pct, days string) {
		e.post("/admin/company", url.Values{
			"name": {"IT-Betrieb"}, "address": {"Hofstraße 2, 4710 Testdorf"},
			"tax_id": {"ATU00000000"}, "tax_mode": {"pauschal"}, "vat_rate": {"13"},
			"payment_term_days": {"14"}, "iban": {"AT611904300234573201"},
			"skonto_pct": {pct}, "skonto_days": {days},
		})
	}
	belegURL := fmt.Sprintf("/neighbors/%d/beleg?year=%d&rechnung=1", nid, yid)

	// Without an issued invoice there is no date to anchor the deadline: no
	// clause. Matched on "% Skonto" — the clause's own wording — because the
	// unissued view redirects to a page whose § 16 help text also says Skonto.
	setSkonto("2", "14")
	if page := e.get(belegURL); strings.Contains(page, "% Skonto") {
		t.Errorf("skonto clause shown without an issued invoice")
	}

	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	if page := e.get(belegURL); !strings.Contains(page, "% Skonto") {
		t.Errorf("skonto clause missing on the issued invoice")
	}

	// Switched off -> gone.
	setSkonto("0", "0")
	if page := e.get(belegURL); strings.Contains(page, "% Skonto") {
		t.Errorf("skonto clause shown although the offer is off")
	}
}
