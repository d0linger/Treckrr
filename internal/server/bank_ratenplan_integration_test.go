package server

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/bankimport"
)

// postMultipart uploads one file through the real handler, with the CSRF token
// as a form field (the middleware reads it from the multipart body too).
func (e *itEnv) postMultipart(path, filename string, content []byte) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf_token", e.csrf("/"))
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		e.t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()
	resp, err := e.client.Post(e.srv.URL+path, mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		e.t.Fatalf("POST %s -> %d", path, resp.StatusCode)
	}
	return string(b)
}

// The bank import gained IBAN matching, manual assignment and camt.054 batch
// splitting (Ausbaukarte 44-46). One statement exercises all three paths:
// reference match, IBAN fallback, and a hand-assigned credit — booked with
// method and invoice link, and de-duplicated on a re-commit.
func TestBankImportMatchingIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// Booking (2 h × 46 = 92,00) and a frozen invoice as the match target.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-10"},
		"hours": {"2"}, "unit": {"h"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	iv, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("invoice: %v", err)
	}

	// Store the payer IBAN on the neighbor — lower case and spaced, so the
	// normalization in the update handler is exercised too.
	e.post(fmt.Sprintf("/neighbors/%d/update", nid), url.Values{
		"name": {"IT-Nachbar " + e.uname}, "note": {""},
		"address": {"Feldweg 1, 4710 Testdorf"}, "tax_id": {""}, "email": {""},
		"iban": {"at61 1904 3002 3457 3201"},
	})
	if n, err := e.st.GetNeighbor(e.ctx, nid); err != nil || n.IBAN != "AT611904300234573201" {
		t.Fatalf("IBAN not normalized/stored: %v %q", err, n.IBAN)
	}

	// camt.054: one Sammler entry that must split (reference match + IBAN match)
	// plus one credit that matches nothing. The nonce keeps every de-dup hash
	// unique across test runs against the shared database.
	nonce := fmt.Sprintf("RUN%d", time.Now().UnixNano())
	statement := fmt.Sprintf(`<?xml version="1.0"?>
<Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.054.001.02">
 <BkToCstmrDbtCdtNtfctn><Ntfctn>
  <Ntry>
    <Amt Ccy="EUR">70.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <AcctSvcrRef>IT-%s-%s</AcctSvcrRef>
    <BookgDt><Dt>2026-05-11</Dt></BookgDt>
    <NtryDtls>
      <TxDtls>
        <Amt Ccy="EUR">40.00</Amt>
        <RmtInf><Ustrd>Zahlung %s %s</Ustrd></RmtInf>
      </TxDtls>
      <TxDtls>
        <Amt Ccy="EUR">30.00</Amt>
        <RmtInf><Ustrd>Danke %s</Ustrd></RmtInf>
        <RltdPties><Dbtr><Nm>IT-Zahler</Nm></Dbtr><DbtrAcct><Id><IBAN>AT611904300234573201</IBAN></Id></DbtrAcct></RltdPties>
      </TxDtls>
    </NtryDtls>
  </Ntry>
  <Ntry>
    <Amt Ccy="EUR">22.00</Amt><CdtDbtInd>CRDT</CdtDbtInd>
    <BookgDt><Dt>2026-05-12</Dt></BookgDt>
    <NtryDtls><TxDtls><RmtInf><Ustrd>Blumen %s</Ustrd></RmtInf></TxDtls></NtryDtls>
  </Ntry>
 </Ntfctn></BkToCstmrDbtCdtNtfctn>
</Document>`, e.uname, nonce, iv.PaymentReference, nonce, nonce, nonce)

	// The imported hashes are not covered by the name-prefix purge (they carry
	// no neighbor link) — clean them up explicitly.
	txns, err := bankimport.Parse([]byte(statement))
	if err != nil || len(txns) != 3 {
		t.Fatalf("statement must parse into 3 credits: %v (%d)", err, len(txns))
	}
	t.Cleanup(func() {
		for _, txn := range txns {
			_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM payment_imports WHERE hash=$1`, txn.Hash)
		}
	})
	var unmatchedHash string
	for _, txn := range txns {
		if txn.Amount.StringFixed(2) == "22.00" {
			unmatchedHash = txn.Hash
		}
	}

	// Preview: reference + IBAN matched, the third row offers manual assignment.
	page := e.postMultipart("/payments/import/preview", "statement.xml", []byte(statement))
	if !strings.Contains(page, "per IBAN") {
		t.Errorf("preview does not show the IBAN-matched row")
	}
	if !strings.Contains(page, "assign_"+unmatchedHash) {
		t.Errorf("preview offers no manual assignment for the unmatched credit")
	}
	if strings.Contains(page, "keine Rechnung gefunden") {
		t.Errorf("unmatched row shows the dead-end tag although assignment is possible")
	}

	// Commit with the manual assignment; all three credits must book.
	e.post("/payments/import", url.Values{
		"raw":                     {statement},
		"assign_" + unmatchedHash: {itoa64(iv.ID)},
	})
	pays, err := e.st.ListPayments(e.ctx, yid, nid)
	if err != nil || len(pays) != 3 {
		t.Fatalf("payments after import: %v (n=%d, want 3)", err, len(pays))
	}
	for _, p := range pays {
		if p.Method != "überweisung" {
			t.Errorf("imported payment has method %q, want überweisung", p.Method)
		}
		if p.InvoiceID == nil || p.InvoiceNumber != iv.Number {
			t.Errorf("imported payment not linked to %s: %v/%q", iv.Number, p.InvoiceID, p.InvoiceNumber)
		}
	}

	// Re-committing the same statement must book nothing (hash de-dup).
	e.post("/payments/import", url.Values{
		"raw":                     {statement},
		"assign_" + unmatchedHash: {itoa64(iv.ID)},
	})
	if pays, _ = e.st.ListPayments(e.ctx, yid, nid); len(pays) != 3 {
		t.Errorf("re-commit booked again: %d payments", len(pays))
	}
}

// The Ratenplan (Ausbaukarte 43) derives installment states from the paid sum:
// past-due and unpaid → überfällig, covered by payments → erledigt.
func TestRatenplanIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-15"},
		"hours": {"2"}, "unit": {"h"},
	})

	// Two agreed installments: 50 was due long ago, 42 far in the future.
	e.post(fmt.Sprintf("/neighbors/%d/installments", nid), url.Values{
		"year_id": {itoa64(yid)}, "due_on": {"2026-01-15"}, "amount": {"50"}, "note": {"1. Rate"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/installments", nid), url.Values{
		"year_id": {itoa64(yid)}, "due_on": {"2099-01-15"}, "amount": {"42"},
	})
	pageURL := fmt.Sprintf("/neighbors/%d?year=%d", nid, yid)
	page := e.get(pageURL)
	if !strings.Contains(page, ">überfällig<") || !strings.Contains(page, ">offen<") {
		t.Fatalf("expected one overdue and one open installment on the page")
	}

	// Paying the first installment flips it to erledigt; the future one stays open.
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"50"}, "paid_on": {"2026-05-16"},
	})
	page = e.get(pageURL)
	if !strings.Contains(page, ">erledigt<") {
		t.Errorf("paid installment not shown as erledigt")
	}
	if strings.Contains(page, ">überfällig<") {
		t.Errorf("overdue tag still shown although the installment is covered")
	}
	if !strings.Contains(page, ">offen<") {
		t.Errorf("future installment lost its open state")
	}

	// Delete the future installment; one row must remain.
	plans, err := e.st.ListInstallments(e.ctx, yid, nid)
	if err != nil || len(plans) != 2 {
		t.Fatalf("installments: %v (n=%d)", err, len(plans))
	}
	e.post(fmt.Sprintf("/installments/%d/delete", plans[1].ID), url.Values{})
	if plans, _ = e.st.ListInstallments(e.ctx, yid, nid); len(plans) != 1 {
		t.Errorf("delete left %d installments, want 1", len(plans))
	}
}
