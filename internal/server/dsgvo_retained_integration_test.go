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
	if _, err := e.st.CreateBelegShare(e.ctx, "share-secret-hash-marker", e.neighborID, e.yearID64,
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
	if len(exported.Recurring) != 1 || len(exported.Mail) != 1 || len(exported.AdditionalDelivery) != 4 {
		t.Fatalf("missing subject records or delivery checklist: %+v", exported)
	}
	if exported.Mail[0].AttachmentBytes != int64(len("attachment-content-marker")) {
		t.Fatal("attachment size not represented")
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
