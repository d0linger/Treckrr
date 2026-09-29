package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/store"
)

// TestMailAdminLifecycleIntegration pins the operator-facing state machine: each
// transport attempt leaves evidence, failed mail can be retried, and ambiguity
// requires an explicit force action.
func TestMailAdminLifecycleIntegration(t *testing.T) {
	ctx := store.WithAuditActor(context.Background(), store.AuditActor{Username: "mail-admin-it"})
	st, pool := scratchStore(t)

	failed, _, err := st.CreateMailIntent(ctx, store.OutboxMail{
		Kind: "beleg", Recipient: "mail-admin-failed@example.invalid",
		Subject: "Retry me", Body: "retained body", DeliveryKey: "it:mail-admin:failed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE mail_outbox SET attempts=5, next_attempt_at=now() WHERE id=$1`, failed.ID); err != nil {
		t.Fatal(err)
	}
	status, err := st.AttemptMail(ctx, failed.ID, func(context.Context, string, string, string, string, string, string, []byte) error {
		return errors.New("permanent SMTP refusal")
	})
	if err != nil || status != store.MailStatusFailed {
		t.Fatalf("terminal attempt: status=%q err=%v", status, err)
	}
	detail, err := st.GetMailOutboxDetail(ctx, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Item.Status != store.MailStatusFailed || len(detail.Attempts) != 1 || detail.Attempts[0].Outcome != "failed" {
		t.Fatalf("failed detail = %+v", detail)
	}
	if _, err := st.RequeueMail(ctx, failed.ID, false); err != nil {
		t.Fatalf("requeue failed mail: %v", err)
	}
	status, err = st.AttemptMail(ctx, failed.ID, func(context.Context, string, string, string, string, string, string, []byte) error {
		return nil
	})
	if err != nil || status != store.MailStatusSent {
		t.Fatalf("manual retry: status=%q err=%v", status, err)
	}
	detail, err = st.GetMailOutboxDetail(ctx, failed.ID)
	if err != nil || len(detail.Attempts) != 2 || detail.Attempts[0].Outcome != "sent" {
		t.Fatalf("attempt history after retry = %+v, %v", detail.Attempts, err)
	}

	ambiguous, _, err := st.CreateMailIntent(ctx, store.OutboxMail{
		Kind: "mahnung", Recipient: "mail-admin-ambiguous@example.invalid",
		Subject: "Maybe sent", Body: "retained body", DeliveryKey: "it:mail-admin:ambiguous",
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err = st.AttemptMail(ctx, ambiguous.ID, func(context.Context, string, string, string, string, string, string, []byte) error {
		return mail.Ambiguous(errors.New("connection lost after DATA"))
	})
	if err != nil || status != store.MailStatusAmbiguous {
		t.Fatalf("ambiguous attempt: status=%q err=%v", status, err)
	}
	if _, err := st.RequeueMail(ctx, ambiguous.ID, false); !errors.Is(err, store.ErrMailStateConflict) {
		t.Fatalf("ambiguous mail requeued without force: %v", err)
	}
	if _, err := st.RequeueMail(ctx, ambiguous.ID, true); err != nil {
		t.Fatalf("force resend ambiguous mail: %v", err)
	}

	var manualRetry, forceResend bool
	if err := pool.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM audit_log WHERE action='mail_manual_retry' AND entity_id='it:mail-admin:failed'),
		       EXISTS(SELECT 1 FROM audit_log WHERE action='mail_force_resend' AND entity_id='it:mail-admin:ambiguous')`).
		Scan(&manualRetry, &forceResend); err != nil {
		t.Fatal(err)
	}
	if !manualRetry || !forceResend {
		t.Fatalf("operator actions not audited: retry=%v force=%v", manualRetry, forceResend)
	}
}

// TestMailAdminRejectsRedactedRetryIntegration keeps retention irreversible:
// metadata remains visible, but deleted message content cannot be resurrected.
func TestMailAdminRejectsRedactedRetryIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)
	m, _, err := st.CreateMailIntent(ctx, store.OutboxMail{
		Kind: "beleg", Recipient: "mail-admin-redacted@example.invalid",
		Subject: "Old failure", Body: "sensitive", DeliveryKey: "it:mail-admin:redacted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `
		UPDATE mail_outbox SET status='failed', terminal_at=now(), redacted_at=now(), body='', att_data=NULL
		 WHERE id=$1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RequeueMail(ctx, m.ID, false); !errors.Is(err, store.ErrMailPayloadUnavailable) {
		t.Fatalf("redacted mail retry error = %v", err)
	}
}
