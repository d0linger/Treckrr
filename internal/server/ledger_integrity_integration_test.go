//go:build integration

package server

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

var paymentKeyRe = regexp.MustCompile(`(?s)action="/neighbors/\d+/payments".*?name="idempotency_key" value="([0-9a-f]{32})"`)

// WEB-09 + LED-06: the payment form carries a per-render key; a resubmitted
// form books once, and sub-cent amounts are rejected with a clear message.
func TestPaymentFormIdempotencyIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	page := e.get(neighborPaymentsURL(nid, yid))
	m := paymentKeyRe.FindStringSubmatch(page)
	if m == nil {
		t.Fatal("payment form renders no idempotency_key")
	}
	if again := paymentKeyRe.FindStringSubmatch(e.get(neighborPaymentsURL(nid, yid))); again == nil || again[1] == m[1] {
		t.Fatal("idempotency_key is not fresh per render")
	}
	form := url.Values{
		"year_id": {itoa64(yid)}, "amount": {"40"}, "paid_on": {"2026-05-02"},
		"method": {"bar"}, "idempotency_key": {m[1]},
	}
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), form)
	body := e.post(fmt.Sprintf("/neighbors/%d/payments", nid), form)
	if !strings.Contains(body, "bereits erfasst") {
		t.Error("resubmission is not reported as already recorded")
	}
	pays, err := e.st.ListPayments(e.ctx, yid, nid)
	if err != nil || len(pays) != 1 {
		t.Fatalf("payments after resubmit: %d (%v), want 1", len(pays), err)
	}

	body = e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"9,995"}, "paid_on": {"2026-05-03"},
	})
	if !strings.Contains(body, "höchstens zwei Nachkommastellen") {
		t.Error("sub-cent payment not rejected with the cents message")
	}
	if pays, _ := e.st.ListPayments(e.ctx, yid, nid); len(pays) != 1 {
		t.Fatalf("sub-cent payment was stored: %d payments", len(pays))
	}
}

// LED-08: a Guthaben can be paid out after the year is completed.
func TestCreditPayoutInCompletedYearIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"25"}, "paid_on": {"2026-05-02"},
	})
	if err := e.st.SetYearStatus(e.ctx, yid, "completed"); err != nil {
		t.Fatalf("complete year: %v", err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/credit-payout", nid), url.Values{"year_id": {itoa64(yid)}})
	sum, err := e.st.NeighborLedgerSum(e.ctx, yid, nid)
	if err != nil || !sum.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("payout posting in completed year = %s (%v), want 25", sum, err)
	}
}

// LED-02: one side of a transfer cannot be edited through the handler, and the
// page offers no edit/copy action for it.
func TestLedgerTransferEditRefusedIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	tid := fmt.Sprintf("srv-transfer-%d", time.Now().UnixNano())
	var targetID int64
	if err := e.pool.QueryRowContext(e.ctx, `
		WITH src AS (
		  INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date, transfer_id)
		  VALUES ($1,$2,-50,'raus',CURRENT_DATE,$3))
		INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date, transfer_id)
		VALUES ($1,$2,50,'rein',CURRENT_DATE,$3) RETURNING id`, yid, nid, tid).Scan(&targetID); err != nil {
		t.Fatalf("transfer fixture: %v", err)
	}
	page := e.get(fmt.Sprintf("/neighbors/%d?year=%d", nid, yid))
	if strings.Contains(page, fmt.Sprintf("/ledger/%d/edit", targetID)) || strings.Contains(page, fmt.Sprintf("/ledger/%d/copy", targetID)) {
		t.Error("transfer row still offers edit/copy")
	}
	body := e.post(fmt.Sprintf("/ledger/%d/update", targetID), url.Values{
		"amount": {"500"}, "direction": {"debit"}, "description": {"manipuliert"}, "posting_date": {"2026-05-01"},
	})
	if !strings.Contains(body, "Übertrag kann nicht einseitig") {
		t.Error("transfer edit not refused with a clear message")
	}
	var amount decimal.Decimal
	if err := e.pool.QueryRowContext(e.ctx, `SELECT amount FROM neighbor_ledger WHERE id=$1`, targetID).Scan(&amount); err != nil ||
		!amount.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("transfer side amount = %s (%v), want 50", amount, err)
	}
}

// LED-05: a neighbor with only an Abschlag stays in the year.
func TestRemoveNeighborWithDocumentRefusedIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	if _, err := e.st.CreateAnzahlung(e.ctx, yid, nid, e.year, decimal.NewFromInt(50), "1. Abschlag", time.Time{}); err != nil {
		t.Fatalf("anzahlung: %v", err)
	}
	body := e.post("/years/remove-neighbor", url.Values{"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)}})
	if !strings.Contains(body, "Belege") {
		t.Error("removal refusal does not name the documents")
	}
	if member, err := e.st.NeighborInYear(e.ctx, yid, nid); err != nil || !member {
		t.Fatalf("neighbor with an Abschlag was removed (%v)", err)
	}
}

// LED-04: the storno confirmation names the attached credit notes it will
// reverse, and the storno issues their reversals.
func TestInvoiceStornoNamesCreditsIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-01"},
		"hours": {"2"}, "unit": {"h"},
	})
	e.postIssue(nid, url.Values{"year_id": {itoa64(yid)}})
	e.post(fmt.Sprintf("/neighbors/%d/invoice/gutschrift", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"5"}, "note": {"Nachlass"},
	})
	credits, err := e.st.ListInvoiceDocuments(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("documents: %v", err)
	}
	creditNo := ""
	for _, d := range credits {
		if d.Kind == "gutschrift" {
			creditNo = d.Number
		}
	}
	if creditNo == "" {
		t.Fatal("credit note fixture missing")
	}
	page := e.get(fmt.Sprintf("/neighbors/%d/beleg?year=%d&rechnung=1", nid, yid))
	if !strings.Contains(page, "auch diese Gutschriften") || !strings.Contains(page, creditNo) {
		t.Error("storno confirmation does not list the attached credit note")
	}
	body := e.post(fmt.Sprintf("/neighbors/%d/invoice/storno", nid), url.Values{"year_id": {itoa64(yid)}, "reason": {""}})
	if !strings.Contains(body, creditNo+"-S") {
		t.Error("storno success message does not name the credit reversal")
	}
}
