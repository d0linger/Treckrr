//go:build integration

package server

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

func TestRepeatNeighborErasureIntegration(t *testing.T) {
	e := newItEnv(t)
	// Anonymization replaces the fixture-name suffix, so the normal suffix
	// purge cannot identify this parent afterward. Remove its financial children
	// first, then the exact owned neighbor while the test pool is still open.
	t.Cleanup(func() {
		e.srv.Close()
		e.purge(e.uname)
		if err := e.st.DeleteNeighbor(e.ctx, e.neighborID); err != nil {
			t.Error(err)
		}
	})
	entryID, err := e.st.CreateEntry(e.ctx, &models.Entry{
		NeighborID: e.neighborID, BillingYearID: e.yearID64, Date: time.Now(),
		TaskLabel: "Personal task", Note: "Personal note", Cost: decimal.NewFromInt(10),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/neighbors/%d/anonymize", e.neighborID)
	e.post(path, url.Values{"confirm": {"ANONYMISIEREN"}})
	page := e.get("/neighbors?scope=anonymisiert")
	if !strings.Contains(page, "Erneut anonymisieren") || !strings.Contains(page, `name="confirm"`) {
		t.Fatal("anonymized neighbor has no typed-confirmation repeat-erasure form")
	}
	if _, err := e.pool.ExecContext(e.ctx,
		`UPDATE entries SET note='legacy personal note', void_reason='legacy personal reason' WHERE id=$1`, entryID); err != nil {
		t.Fatal(err)
	}
	e.post(path, url.Values{"confirm": {"anonymisieren"}})
	entry, err := e.st.GetEntry(e.ctx, entryID)
	if err != nil || entry.Note != "legacy personal note" {
		t.Fatalf("incorrect confirmation erased data: %+v (%v)", entry, err)
	}
	e.post(path, url.Values{"confirm": {"ANONYMISIEREN"}})
	entry, err = e.st.GetEntry(e.ctx, entryID)
	if err != nil || entry.Note != "" || entry.VoidReason != "" || !entry.Cost.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("repeat handler failed erasure or changed the amount: %+v (%v)", entry, err)
	}
}

func TestDsgvoRetainedRecordsIntegration(t *testing.T) {
	e := newItEnv(t)
	if err := e.st.AddPayment(e.ctx, e.yearID64, e.neighborID, decimal.NewFromInt(12),
		time.Now(), "retained-payment-marker", "bar"); err != nil {
		t.Fatal(err)
	}
	payments, err := e.st.ListPayments(e.ctx, e.yearID64, e.neighborID)
	if err != nil || len(payments) != 1 {
		t.Fatalf("payment fixture: %v (%v)", payments, err)
	}
	if changed, err := e.st.DeletePayment(e.ctx, payments[0].ID); err != nil || !changed {
		t.Fatalf("delete fixture payment: %v, %v", changed, err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `
		INSERT INTO neighbor_ledger (billing_year_id,neighbor_id,amount,posting_date,description,voided,void_reason)
		VALUES ($1,$2,1,now(),'ledger-description',true,'ledger-void-marker')`, e.yearID64, e.neighborID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `
		INSERT INTO recurring_entries (neighbor_id,template,interval_kind,next_run)
		VALUES ($1,'{"task_label":"recurrence-only-marker","note":"recurring-note"}'::jsonb,'monthly',now())`, e.neighborID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.EnqueueMail(e.ctx, store.OutboxMail{
		Kind: "beleg", NeighborID: e.neighborID, Recipient: "subject@example.invalid",
		Subject: "retained-mail-marker", Body: "mail-body-marker", AttName: "subject.pdf",
		AttType: "application/pdf", AttData: []byte("attachment-content-marker"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.ExecContext(e.ctx,
		`UPDATE mail_outbox SET status='failed',last_error='smtp-secret-marker' WHERE neighbor_id=$1`, e.neighborID); err != nil {
		t.Fatal(err)
	}
	// Retained legacy metadata may predate invoice binding. Only the synthetic
	// fixture writes an unbound row; the public share writer requires an invoice.
	if _, err := e.pool.ExecContext(e.ctx, `INSERT INTO beleg_shares
		(token_hash,neighbor_id,billing_year_id,expires_at,created_by)
		VALUES ($1,$2,$3,$4,$5)`, "share-secret-hash-marker", e.neighborID, e.yearID64,
		time.Now().Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	// The fixture's suffix cleanup also owns this neighbor, which deliberately
	// has no billing-year membership or financial records.
	onlyRecurringID, err := e.st.CreateNeighbor(e.ctx, "Only recurring "+e.uname, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.ExecContext(e.ctx, `
		INSERT INTO recurring_entries (neighbor_id,template,interval_kind,next_run)
		VALUES ($1,'{"note":"unrelated-template-marker"}'::jsonb,'weekly',now())`, onlyRecurringID); err != nil {
		t.Fatal(err)
	}
	// A non-neighbor operational message must not leak into this subject export.
	if err := e.st.EnqueueMail(e.ctx, store.OutboxMail{
		Kind: "beleg", Subject: "unrelated-mail-marker", Recipient: "other@example.invalid", Body: "other-body",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := e.pool.ExecContext(e.ctx,
			`DELETE FROM mail_outbox WHERE neighbor_id IS NULL AND subject='unrelated-mail-marker'`); err != nil {
			t.Error(err)
		}
	})
	body := e.get(fmt.Sprintf("/neighbors/%d/dsgvo-export.json", e.neighborID))
	var exported dsgvoExport
	if err := json.Unmarshal([]byte(body), &exported); err != nil {
		t.Fatalf("export JSON: %v", err)
	}
	for _, marker := range []string{
		"retained-payment-marker", "ledger-void-marker", "recurrence-only-marker",
		"mail-body-marker", "subject.pdf", `"status": "failed"`, `"deleted_at":`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("export missing %q", marker)
		}
	}
	for _, excluded := range []string{
		"unrelated-mail-marker", "other@example.invalid", "smtp-secret-marker",
		"attachment-content-marker", "share-secret-hash-marker", "unrelated-template-marker",
	} {
		if strings.Contains(body, excluded) {
			t.Errorf("export leaked excluded value %q", excluded)
		}
	}
	if len(exported.Recurring) != 1 || len(exported.Mail) != 1 || len(exported.AdditionalDelivery) != 5 ||
		!strings.Contains(strings.Join(exported.AdditionalDelivery, "\n"), "Unterschriften:") {
		t.Fatalf("missing subject records or delivery checklist: %+v", exported)
	}
	if exported.Mail[0].AttachmentBytes != int64(len("attachment-content-marker")) {
		t.Fatal("attachment size not represented")
	}
	if exported.Mail[0].InvoiceID != nil || len(exported.BillingYears) != 1 ||
		len(exported.BillingYears[0].Payments) != 1 || exported.BillingYears[0].Payments[0].PreferredInvoiceID != nil ||
		len(exported.BillingYears[0].ShareLinks) != 1 || exported.BillingYears[0].ShareLinks[0].InvoiceID != nil {
		t.Fatal("legacy unbound metadata acquired an invoice assignment")
	}
	if active, err := e.st.ListPayments(e.ctx, e.yearID64, e.neighborID); err != nil || len(active) != 0 {
		t.Fatalf("subject export changed active payment semantics: %v (%v)", active, err)
	}
	var templateOnly dsgvoExport
	body = e.get(fmt.Sprintf("/neighbors/%d/dsgvo-export.json", onlyRecurringID))
	if err := json.Unmarshal([]byte(body), &templateOnly); err != nil {
		t.Fatal(err)
	}
	if len(templateOnly.BillingYears) != 0 || len(templateOnly.Recurring) != 1 || len(templateOnly.Mail) != 0 {
		t.Fatalf("template-only neighbor export is incomplete or crossed subjects: %+v", templateOnly)
	}
	if templateOnly.Recurring[0].Template.Note != "unrelated-template-marker" {
		t.Fatal("template-only neighbor lost its stored note")
	}
}

func TestDsgvoInvoiceMetadataIntegration(t *testing.T) {
	e := newItEnv(t)
	issuedOn := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	issue := func(neighborID int64, amount int64) (int64, models.Invoice) {
		t.Helper()
		entryID, err := e.st.CreateEntry(e.ctx, &models.Entry{
			NeighborID: neighborID, BillingYearID: e.yearID64, Date: issuedOn,
			TaskLabel: "Subject export source", Unit: "h", Hours: decimal.NewFromInt(1),
			HourlyRate: decimal.NewFromInt(amount), Cost: decimal.NewFromInt(amount),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		invoice, err := e.st.IssueInvoice(e.ctx, e.yearID64, neighborID, e.year, issuedOn)
		if err != nil {
			t.Fatal(err)
		}
		return entryID, invoice
	}
	firstEntry, first := issue(e.neighborID, 100)
	secondEntry, second := issue(e.neighborID, 200)
	otherID, err := e.st.CreateNeighbor(e.ctx, "Other export subject "+e.uname, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddNeighborToYear(e.ctx, e.yearID64, otherID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.UpdateNeighbor(e.ctx, otherID, "Other export subject "+e.uname, "",
		"Other test lane 2", "", "", "", nil); err != nil {
		t.Fatal(err)
	}
	_, otherInvoice := issue(otherID, 300)
	payment, err := e.st.RecordPayment(e.ctx, store.PaymentInput{
		YearID: e.yearID64, NeighborID: e.neighborID, InvoiceID: second.ID,
		Amount: decimal.NewFromInt(12), PaidOn: issuedOn, Note: "Selected retained payment",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := e.st.DeletePayment(e.ctx, payment.PaymentID); err != nil || !changed {
		t.Fatalf("retain selected payment: changed=%v, err=%v", changed, err)
	}
	if err := e.st.RecordInvoiceBelegSend(e.ctx, e.yearID64, e.neighborID, second.ID, "manuell"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateInvoiceBelegShare(e.ctx, "invoice-share-secret-marker", e.neighborID,
		e.yearID64, second.ID, time.Now().Add(time.Hour), "test"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.EnqueueMail(e.ctx, store.OutboxMail{
		Kind: "beleg", NeighborID: e.neighborID, BillingYearID: e.yearID64, InvoiceID: second.ID,
		Recipient: "subject@example.invalid", Subject: "Selected invoice export", Body: "subject correspondence",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.StornoInvoiceByID(e.ctx, e.yearID64, e.neighborID, first.ID, "Export history test"); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteEntry(e.ctx, firstEntry); err != nil {
		t.Fatal(err)
	}
	before, err := e.st.AccountRemaining(e.ctx, e.yearID64, e.neighborID)
	if err != nil {
		t.Fatal(err)
	}
	body := e.get(fmt.Sprintf("/neighbors/%d/dsgvo-export.json", e.neighborID))
	var exported dsgvoExport
	if err := json.Unmarshal([]byte(body), &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported.BillingYears) != 1 || len(exported.Mail) != 1 {
		t.Fatalf("unexpected export scope: years=%d mail=%d", len(exported.BillingYears), len(exported.Mail))
	}
	year := exported.BillingYears[0]
	wantInvoice := func(id *int64) bool { return id != nil && *id == second.ID }
	if len(year.Payments) != 1 || !wantInvoice(year.Payments[0].InvoiceID) ||
		!wantInvoice(year.Payments[0].PreferredInvoiceID) || year.Payments[0].DeletedAt == nil ||
		!year.Payments[0].Amount.Equal(decimal.NewFromInt(12)) {
		t.Fatalf("retained payment selection/value lost: %+v", year.Payments)
	}
	if len(year.Sends) != 1 || !wantInvoice(year.Sends[0].InvoiceID) ||
		len(year.ShareLinks) != 1 || !wantInvoice(year.ShareLinks[0].InvoiceID) ||
		!wantInvoice(exported.Mail[0].InvoiceID) {
		t.Fatal("document-bound delivery metadata missing")
	}
	if len(year.Entries) != 1 || year.Entries[0].ID != secondEntry || len(year.InvoiceSources) != 2 {
		t.Fatalf("source identities lost or crossed subjects: entries=%+v sources=%+v", year.Entries, year.InvoiceSources)
	}
	invoices := make(map[int64]dsgvoInvoice)
	for _, invoice := range year.Invoices {
		invoices[invoice.ID] = invoice
		if invoice.ID == otherInvoice.ID {
			t.Fatal("another subject's invoice leaked")
		}
	}
	for _, source := range year.InvoiceSources {
		if _, ok := invoices[source.InvoiceID]; !ok || source.Ordinal != 1 {
			t.Fatalf("unresolvable invoice provenance: %+v", source)
		}
		switch source.InvoiceID {
		case first.ID:
			if source.Active || source.EntryID != nil || source.SourceEntryID != firstEntry {
				t.Fatalf("released/deleted source history changed: %+v", source)
			}
		case second.ID:
			if !source.Active || source.EntryID == nil || *source.EntryID != secondEntry || source.SourceEntryID != secondEntry {
				t.Fatalf("active source history changed: %+v", source)
			}
		default:
			t.Fatalf("another subject's source leaked: %+v", source)
		}
	}
	if sources, err := e.st.ListNeighborInvoiceSourceExport(e.ctx, e.yearID64+1000000, e.neighborID); err != nil || len(sources) != 0 {
		t.Fatalf("source export ignored billing-year scope: %+v (%v)", sources, err)
	}
	if strings.Contains(body, "invoice-share-secret-marker") {
		t.Fatal("export leaked a share credential hash")
	}
	after, err := e.st.AccountRemaining(e.ctx, e.yearID64, e.neighborID)
	if err != nil || !after.Equal(before) {
		t.Fatalf("read-only export changed account balance: before=%s after=%s err=%v", before, after, err)
	}
}
