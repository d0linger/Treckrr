//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/testutil/pdftext"
	"github.com/d0linger/treckrr/internal/web"
)

func TestMultipleInvoiceDocumentIsolationIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	company, err := e.st.GetCompany(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	company.IBAN = "AT611904300234573201"
	if err := e.st.UpdateCompany(e.ctx, company); err != nil {
		t.Fatal(err)
	}
	// Both names keep the fixture suffix, including when the test fails before
	// the second rename, so repeated runs cannot leave an unowned neighbor.
	if err := e.st.UpdateNeighbor(e.ctx, nid, "Frozen recipient A "+e.uname, "", "Frozen address A", "", "synthetic@example.invalid", "", nil); err != nil {
		t.Fatal(err)
	}
	book := func(label, hours string) {
		e.post("/entries", url.Values{
			"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)}, "gespann_id": {itoa64(e.gespannID)},
			"entry_date": {"2026-03-01"}, "hours": {hours}, "unit": {"h"}, "task_label": {label},
		})
	}
	book("ONLY DOCUMENT A", "1")
	e.postIssue(nid, url.Values{"year_id": {itoa64(yid)}})
	first, err := e.st.GetInvoice(e.ctx, yid, nid)
	if err != nil || first.Content == nil {
		t.Fatalf("first invoice: %v", err)
	}
	frozen, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	archiveA, err := pdf.RenderInvoice(&first)
	if err != nil {
		t.Fatal(err)
	}
	documentServer := &Server{store: e.st}
	firstView, err := documentServer.loadInvoiceDocumentView(e.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	pdfA, err := (documentService{}).invoicePDF(firstView)
	if err != nil {
		t.Fatal(err)
	}
	documentText := func(blob []byte) string {
		t.Helper()
		text, err := pdftext.Text(blob)
		if err != nil {
			t.Fatal(err)
		}
		// Independent downloads have their own as-of timestamp. All other
		// rendered content, including settlement, must match the selected view.
		return regexp.MustCompile(`(?m)^Zahlungsstand vom[^\r\n]*`).ReplaceAllString(text, "")
	}
	pdfAText := documentText(pdfA)
	sharePage := e.post(fmt.Sprintf("/neighbors/%d/beleg/share?year=%d&invoice_id=%d", nid, yid, first.ID), url.Values{"days": {"30"}})
	match := regexp.MustCompile(`/s/portal/([a-f0-9]+)`).FindStringSubmatch(sharePage)
	if len(match) != 2 {
		t.Fatal("fresh selected invoice share was not displayed")
	}
	token := match[1]
	book("ONLY DOCUMENT B", "2")
	e.postIssue(nid, url.Values{"year_id": {itoa64(yid)}})
	invoices, err := e.st.ListActiveInvoices(e.ctx, yid, nid)
	if err != nil || len(invoices) != 2 {
		t.Fatalf("two active invoices: count=%d, err=%v", len(invoices), err)
	}
	second := invoices[1]
	book("PENDING WORK C", "3")
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); !errors.Is(err, store.ErrInvoiceAmbiguous) {
		t.Fatalf("unselected multi-invoice lookup: %v", err)
	}

	// Mutating master data and adding new work cannot rewrite frozen parties,
	// lines or rates in either document consumer.
	company.Name, company.Address, company.IBAN = "LIVE ISSUER MUST NOT LEAK", "LIVE ADDRESS", "AT483200000012345864"
	if err := e.st.UpdateCompany(e.ctx, company); err != nil {
		t.Fatal(err)
	}
	// Retain the fixture suffix so cleanup also finds the renamed neighbor.
	if err := e.st.UpdateNeighbor(e.ctx, nid, "LIVE RECIPIENT MUST NOT LEAK "+e.uname, "", "LIVE RECIPIENT ADDRESS", "", "synthetic@example.invalid", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		invoice      models.Invoice
		want, absent string
	}{{first, "ONLY DOCUMENT A", "ONLY DOCUMENT B"}, {second, "ONLY DOCUMENT B", "ONLY DOCUMENT A"}} {
		page := e.get(invoiceDocumentURL(tc.invoice))
		for _, want := range []string{tc.want, "Frozen recipient A", tc.invoice.Number} {
			if !strings.Contains(page, want) {
				t.Fatalf("selected invoice %d lacks %q", tc.invoice.ID, want)
			}
		}
		for _, forbidden := range []string{tc.absent, "PENDING WORK C", "LIVE ISSUER MUST NOT LEAK", "LIVE RECIPIENT MUST NOT LEAK"} {
			if strings.Contains(page, forbidden) {
				t.Fatalf("selected invoice %d leaked %q", tc.invoice.ID, forbidden)
			}
		}
	}
	pending, err := e.st.BuildPendingInvoiceContent(e.ctx, yid, nid)
	if err != nil {
		t.Fatal(err)
	}
	expectedAccount := first.Content.Gross.Add(second.Content.Gross).Add(pending.Net)
	annual := e.get(fmt.Sprintf("/neighbors/%d/beleg?year=%d", nid, yid))
	if !strings.Contains(annual, `class="beleg__hv beleg__sum-informal">`+web.Money(expectedAccount)) {
		t.Fatal("annual payable omits issued VAT or pending net work")
	}
	if !strings.Contains(annual, "USt aus festgeschriebenen Rechnungen") ||
		!strings.Contains(annual, web.Money(first.Content.VATAmount.Add(second.Content.VATAmount))) {
		t.Fatal("annual overview does not explain frozen VAT")
	}
	for _, want := range []string{"ONLY DOCUMENT A", "ONLY DOCUMENT B", "PENDING WORK C"} {
		if !strings.Contains(annual, want) {
			t.Errorf("annual overview missing %q", want)
		}
	}
	get := func(client *http.Client, path string, status int) []byte {
		t.Helper()
		response, err := client.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != status {
			t.Fatalf("GET %s: status=%d want=%d", path, response.StatusCode, status)
		}
		return body
	}
	selectedQuery := fmt.Sprintf("?year=%d&invoice_id=%d", yid, first.ID)
	if got := get(e.client, fmt.Sprintf("/neighbors/%d/beleg.pdf", nid)+selectedQuery, 200); documentText(got) != pdfAText {
		t.Fatal("selected PDF changed without any settlement change")
	}
	for _, path := range []string{fmt.Sprintf("/neighbors/%d/beleg?year=%d&rechnung=1", nid, yid),
		fmt.Sprintf("/neighbors/%d/beleg.pdf?year=%d", nid, yid),
		fmt.Sprintf("/neighbors/%d/beleg?year=%d&invoice_id=broken", nid, yid),
		fmt.Sprintf("/neighbors/%d/beleg?year=%d&invoice_id=%d&invoice_id=%d", nid, yid, first.ID, second.ID)} {
		get(e.client, path, http.StatusBadRequest)
	}
	get(e.client, fmt.Sprintf("/neighbors/%d/beleg?year=%d&invoice_id=9223372036854775807", nid, yid), http.StatusNotFound)

	e.post(fmt.Sprintf("/neighbors/%d/beleg/email", nid)+selectedQuery, url.Values{})
	var outboxInvoiceID int64
	var attachment []byte
	if err := e.pool.QueryRowContext(e.ctx, `SELECT invoice_id,att_data FROM mail_outbox WHERE delivery_key=$1`,
		fmt.Sprintf("beleg:%d:synthetic@example.invalid", first.ID)).Scan(&outboxInvoiceID, &attachment); err != nil {
		t.Fatal(err)
	}
	if outboxInvoiceID != first.ID || documentText(attachment) != pdfAText {
		t.Fatal("mail intent references a different invoice or PDF")
	}
	e.post(fmt.Sprintf("/neighbors/%d/beleg/mark-sent", nid)+selectedQuery, url.Values{})
	if send, err := e.st.LastInvoiceBelegSend(e.ctx, yid, nid, second.ID); err != nil || send != nil {
		t.Fatalf("A delivery marked B as sent: %v", err)
	}
	if send, err := e.st.LastInvoiceBelegSend(e.ctx, yid, nid, first.ID); err != nil || send == nil || send.InvoiceID == nil || *send.InvoiceID != first.ID {
		t.Fatalf("missing selected manual delivery metadata: %v", err)
	}

	// FIFO is the default; explicit selection applies only to the selected bill.
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"10"}, "invoice_id": {""}, "paid_on": {"2026-03-02"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"10"}, "invoice_id": {itoa64(second.ID)}, "paid_on": {"2026-03-03"},
	})
	for _, invoice := range invoices {
		remaining, err := e.st.InvoiceDocumentRemaining(e.ctx, invoice.ID)
		if err != nil || !remaining.Equal(invoice.Content.Gross.Sub(decimal.NewFromInt(10))) {
			t.Fatalf("selected/FIFO remaining for %d: %s, %v", invoice.ID, remaining, err)
		}
	}
	paidAnnual := e.get(fmt.Sprintf("/neighbors/%d/beleg?year=%d", nid, yid))
	if !strings.Contains(paidAnnual, `class="beleg__hv beleg__sum-informal">`+web.Money(expectedAccount.Sub(decimal.NewFromInt(20)))) {
		t.Fatal("annual payable after payments differs from the authoritative account balance")
	}
	e.post(fmt.Sprintf("/neighbors/%d/invoice/gutschrift", nid), url.Values{
		"year_id": {itoa64(yid)}, "invoice_id": {itoa64(first.ID)}, "amount": {"5"}, "note": {"Synthetic document credit"},
	})
	documents, err := e.st.ListInvoiceDocuments(e.ctx, yid, nid)
	if err != nil {
		t.Fatal(err)
	}
	credits := attachedInvoiceCredits(first, documents)
	if len(credits) != 1 {
		t.Fatalf("selected credit fixture: got %d credits", len(credits))
	}
	firstPage := e.get(invoiceDocumentURL(first))
	if !strings.Contains(firstPage, "auch diese Gutschriften") || !strings.Contains(firstPage, credits[0].Number) {
		t.Fatal("selected storno confirmation omits the affected credit")
	}
	if strings.Contains(e.get(invoiceDocumentURL(second)), credits[0].Number) {
		t.Fatal("another invoice's storno confirmation exposes the selected credit")
	}
	creditedAnnual := e.get(fmt.Sprintf("/neighbors/%d/beleg?year=%d", nid, yid))
	if !strings.Contains(creditedAnnual, `class="beleg__hv beleg__sum-informal">`+web.Money(expectedAccount.Sub(decimal.NewFromInt(25)))) {
		t.Fatal("annual payable did not apply the selected credit once")
	}
	if !strings.Contains(creditedAnnual, "Gutschriften") || !strings.Contains(creditedAnnual, "−5,00 €") && !strings.Contains(creditedAnnual, "-5,00 €") {
		t.Fatal("annual overview does not explain the document credit")
	}
	remaining, err := e.st.InvoiceDocumentRemaining(e.ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	reference := first.PaymentReference
	if reference == "" {
		reference = first.Number
	}
	expectedQR, err := qrPNG(epcPayload(first.Content.Issuer.Name, first.Content.Issuer.IBAN, remaining, reference))
	if err != nil {
		t.Fatal(err)
	}
	if got := get(e.client, fmt.Sprintf("/neighbors/%d/invoice/epc-qr.png", nid)+selectedQuery, 200); !bytes.Equal(got, expectedQR) {
		t.Fatal("QR changed party, reference or document-specific remaining")
	}

	public := &http.Client{Timeout: 10 * time.Second}
	portalPath := "/s/portal/" + token
	for _, result := range []string{"confirmed", "disputed", "invalid", ""} {
		feedbackPage := string(get(public, portalPath+"?feedback="+result, 200))
		want := result == "confirmed" || result == "disputed"
		if got := strings.Contains(feedbackPage, "Die Rückmeldung wurde gespeichert."); got != want {
			t.Errorf("feedback=%q: confirmation shown=%v, want %v", result, got, want)
		}
	}
	publicHTML := string(get(public, portalPath+"?invoice_id="+itoa64(second.ID), 200))
	if !strings.Contains(publicHTML, "ONLY DOCUMENT A") || strings.Contains(publicHTML, "ONLY DOCUMENT B") ||
		strings.Contains(publicHTML, "PENDING WORK C") || strings.Contains(publicHTML, "LIVE RECIPIENT") {
		t.Fatal("bound share exposed other/new/live content")
	}
	updatedView, err := documentServer.loadInvoiceDocumentView(e.ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	updatedPDF, err := (documentService{}).invoicePDF(updatedView)
	if err != nil {
		t.Fatal(err)
	}
	if documentText(updatedPDF) == pdfAText {
		t.Fatal("customer PDF omitted updated payment and credit settlement")
	}
	if documentText(get(public, portalPath+"/invoice.pdf?invoice_id="+itoa64(second.ID), 200)) != documentText(updatedPDF) {
		t.Fatal("public PDF was retargeted or omitted the selected invoice settlement")
	}
	text, err := pdftext.Text(updatedPDF)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ONLY DOCUMENT A", "Bereits bezahlt / verrechnet", "15,00 €", web.Money(remaining)} {
		if !strings.Contains(text, want) {
			t.Errorf("settled customer PDF missing %q", want)
		}
	}
	for _, forbidden := range []string{"ONLY DOCUMENT B", "PENDING WORK C", "LIVE RECIPIENT", "LIVE ISSUER"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("settled customer PDF leaked %q", forbidden)
		}
	}
	if !bytes.Equal(get(public, portalPath+"/epc-qr.png", 200), expectedQR) {
		t.Fatal("public QR differs from selected document QR")
	}
	e.post(portalPath+"/feedback", url.Values{"status": {"confirmed"}, "invoice_id": {itoa64(second.ID)}})
	feedback, err := e.st.ListBelegFeedback(e.ctx, nid, yid)
	if err != nil || len(feedback) != 1 || feedback[0].InvoiceID != first.ID {
		t.Fatalf("feedback not bound to shared invoice: %v", err)
	}
	unchanged, err := e.st.GetInvoiceDocument(e.ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	now, err := json.Marshal(unchanged)
	if err != nil || !bytes.Equal(now, frozen) {
		t.Fatal("existing invoice values changed after new work, delivery or payments")
	}
	archiveAfter, err := pdf.RenderInvoice(&unchanged)
	if err != nil || !bytes.Equal(archiveAfter, archiveA) {
		t.Fatal("archive PDF changed after new work, delivery or payments")
	}

	// Delivery completion retains the exact invoice identity loaded from the
	// queue. The fake sender touches no SMTP service.
	delivery, err := (documentService{smtpFrom: "synthetic@example.invalid"}).invoiceMail(
		company, models.Neighbor{ID: nid, Email: "synthetic@example.invalid"}, yid, updatedView)
	if err != nil {
		t.Fatal(err)
	}
	delivery.Outbox.DeliveryKey = fmt.Sprintf("integration-selected-delivery:%d", first.ID)
	intent, created, err := e.st.CreateMailIntent(e.ctx, delivery.Outbox)
	if err != nil || !created {
		t.Fatalf("create isolated delivery: %v", err)
	}
	status, err := e.st.AttemptMail(e.ctx, intent.ID,
		func(context.Context, string, string, string, string, string, string, []byte) error { return nil })
	if err != nil || status != store.MailStatusSent {
		t.Fatalf("complete isolated delivery: status=%s err=%v", status, err)
	}
	if send, err := e.st.LastInvoiceBelegSend(e.ctx, yid, nid, first.ID); err != nil || send == nil || send.Channel != "e-mail" {
		t.Fatalf("completed mail lacks document-specific delivery evidence: %v", err)
	}
	if send, err := e.st.LastInvoiceBelegSend(e.ctx, yid, nid, second.ID); err != nil || send != nil {
		t.Fatalf("completed A mail contaminated B delivery evidence: %v", err)
	}

	// A canceled link must not resolve to the still-active second invoice.
	e.post(fmt.Sprintf("/neighbors/%d/invoice/storno", nid), url.Values{
		"year_id": {itoa64(yid)}, "invoice_id": {itoa64(first.ID)}, "reason": {"Synthetic isolation correction"},
	})
	get(public, portalPath, http.StatusNotFound)
	get(public, portalPath+"/invoice.pdf", http.StatusNotFound)
	get(public, portalPath+"/epc-qr.png", http.StatusNotFound)
	get(e.client, fmt.Sprintf("/neighbors/%d/invoice/epc-qr.png", nid)+selectedQuery, http.StatusNotFound)
	canceledPDF := get(e.client, fmt.Sprintf("/neighbors/%d/beleg.pdf", nid)+selectedQuery, http.StatusOK)
	canceledText, err := pdftext.Text(canceledPDF)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(canceledText, "Storniert - keine Zahlung erforderlich.") ||
		!strings.Contains(canceledText, "ONLY DOCUMENT A") {
		t.Fatal("authenticated canceled invoice lost its content or cancellation notice")
	}
	for _, forbidden := range []string{"Noch zu überweisen", "Bereits bezahlt / verrechnet", "ONLY DOCUMENT B"} {
		if strings.Contains(canceledText, forbidden) {
			t.Errorf("canceled invoice PDF contains an inappropriate payment demand or other document: %q", forbidden)
		}
	}
	// An already-loaded issued snapshot may race with cancellation. Settlement
	// loading must fail closed instead of issuing a stale payment demand.
	if _, err := documentServer.loadInvoiceDocumentView(e.ctx, first); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("settlement load for stale issued snapshot: got %v, want not found", err)
	}
	stillSecond, err := e.st.GetInvoiceDocument(e.ctx, second.ID)
	if err != nil || stillSecond.Content.Hash != second.Content.Hash || stillSecond.Status != "issued" {
		t.Fatal("canceling the first invoice changed the second")
	}
}
