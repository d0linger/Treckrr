package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/d0linger/treckrr/internal/store"
)

// seedOutbox inserts one intent in the given state and returns its id.
func seedOutbox(t *testing.T, st *store.Store, recipient, status string) int64 {
	t.Helper()
	ctx := context.Background()
	m, _, err := st.CreateMailIntent(ctx, store.OutboxMail{
		Kind: "beleg", Recipient: recipient, Subject: "Rechnung " + status, Body: "b",
		DeliveryKey: "it:hold:" + recipient,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

// TestRestoreHoldsRestoredOutboxMailIntegration verifies a restore parks every
// pending/sending intent for operator release, and release/discard settle
// them with an audit line.
func TestRestoreHoldsRestoredOutboxMailIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)
	pending := seedOutbox(t, st, "pending@example.invalid", "pending")
	sending := seedOutbox(t, st, "sending@example.invalid", "sending")
	sent := seedOutbox(t, st, "sent@example.invalid", "sent")
	if _, err := pool.ExecContext(ctx, `UPDATE mail_outbox SET attempts=6 WHERE id=$1`, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE mail_outbox SET status='sending', claimed_at=now() WHERE id=$1`, sending); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE mail_outbox SET status='sent', sent_at=now() WHERE id=$1`, sent); err != nil {
		t.Fatal(err)
	}
	if err := st.ReconcileAfterRestore(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	statusOf := func(id int64) string {
		var s string
		if err := pool.QueryRowContext(ctx, `SELECT status FROM mail_outbox WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if statusOf(pending) != "held" || statusOf(sending) != "held" || statusOf(sent) != "sent" {
		t.Fatalf("statuses after restore: %s %s %s", statusOf(pending), statusOf(sending), statusOf(sent))
	}
	if n, err := st.HeldMailCount(ctx); err != nil || n != 2 {
		t.Fatalf("held count = %d, %v", n, err)
	}
	var audited bool
	if err := pool.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM audit_log WHERE action='mail_held_after_restore' AND detail LIKE '2 %')`).Scan(&audited); err != nil || !audited {
		t.Fatalf("no hold audit line (err=%v)", err)
	}

	// Held mail is never delivered automatically, even when due.
	if _, err := pool.ExecContext(ctx, `UPDATE mail_outbox SET next_attempt_at=now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		t.Fatal("held mail was delivered without release")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A new request for the same document reports the held state.
	again, created, err := st.CreateMailIntent(ctx, store.OutboxMail{
		Kind: "beleg", Recipient: "pending@example.invalid", Subject: "x", Body: "b",
		DeliveryKey: "it:hold:pending@example.invalid", RetryFailed: true, ForceResend: true,
	})
	if err != nil || created || again.Status != store.MailStatusHeld {
		t.Fatalf("held intent re-request: status=%q created=%v err=%v", again.Status, created, err)
	}

	held, err := st.ListHeldMail(ctx, 10)
	if err != nil || len(held) != 2 || held[0].ID != pending {
		t.Fatalf("held list = %+v, %v", held, err)
	}
	released, err := st.ReleaseHeldMail(ctx, pending)
	if err != nil {
		t.Fatal(err)
	}
	if released.Attempts != 0 {
		t.Fatalf("released mail retained %d attempts, want 0", released.Attempts)
	}
	if _, err := st.DiscardHeldMail(ctx, sending); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReleaseHeldMail(ctx, sending); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("release of a discarded intent: %v", err)
	}
	delivered, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		return nil
	})
	if err != nil || delivered != 1 || statusOf(pending) != "sent" || statusOf(sending) != "failed" {
		t.Fatalf("after decisions: delivered=%d err=%v pending=%s sending=%s",
			delivered, err, statusOf(pending), statusOf(sending))
	}
	for _, action := range []string{"mail_held_released", "mail_held_discarded"} {
		if err := pool.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM audit_log WHERE action=$1)`, action).Scan(&audited); err != nil || !audited {
			t.Fatalf("no %s audit line (err=%v)", action, err)
		}
	}
}

// TestStaleClaimBeforeDataIsRetriedIntegration verifies a crashed claim that
// never reached SMTP DATA returns to the queue, while claims that did (or that
// an older binary made without a phase) stay terminal and ambiguous.
func TestStaleClaimBeforeDataIsRetriedIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)
	claimed := seedOutbox(t, st, "claimed@example.invalid", "claimed")
	data := seedOutbox(t, st, "data@example.invalid", "data")
	legacy := seedOutbox(t, st, "legacy@example.invalid", "legacy")
	exhausted := seedOutbox(t, st, "exhausted@example.invalid", "exhausted")
	for id, phase := range map[int64]any{claimed: "claimed", data: "data", legacy: nil} {
		if _, err := pool.ExecContext(ctx, `
			UPDATE mail_outbox
			   SET status='sending', attempts=1, delivery_phase=$2,
			       claimed_at=now() - interval '10 minutes'
			 WHERE id=$1`, id, phase); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET status='sending', attempts=100, delivery_phase='claimed',
		       claimed_at=now() - interval '10 minutes'
		 WHERE id=$1`, exhausted); err != nil {
		t.Fatal(err)
	}
	var sentTo []string
	delivered, _, err := st.ProcessMailOutbox(ctx, func(_ context.Context, _, to, _, _, _, _ string, _ []byte) error {
		sentTo = append(sentTo, to)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivered != 1 || len(sentTo) != 1 || sentTo[0] != "claimed@example.invalid" {
		t.Fatalf("delivered=%d to=%v", delivered, sentTo)
	}
	for id, want := range map[int64]string{
		claimed: "sent", data: "ambiguous", legacy: "ambiguous", exhausted: "failed",
	} {
		var got string
		if err := pool.QueryRowContext(ctx, `SELECT status FROM mail_outbox WHERE id=$1`, id).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("intent %d status=%s, want %s", id, got, want)
		}
	}
	var failedAudits int
	if err := pool.QueryRowContext(ctx, `
		SELECT count(*) FROM audit_log
		 WHERE action='mail_retry_failed' AND entity_id='it:hold:exhausted@example.invalid'`).Scan(&failedAudits); err != nil {
		t.Fatal(err)
	}
	if failedAudits != 1 {
		t.Fatalf("stale exhausted mail audit count = %d, want 1", failedAudits)
	}
}

// TestOutboxClaimUsesDatabaseClockIntegration verifies the claim records its
// phase before the transport runs and the retry backoff is computed on the
// database clock that the due query compares against.
func TestOutboxClaimUsesDatabaseClockIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)
	id := seedOutbox(t, st, "clock@example.invalid", "clock")
	var phase string
	status, err := st.AttemptMail(ctx, id, func(context.Context, string, string, string, string, string, string, []byte) error {
		if err := pool.QueryRowContext(ctx, `SELECT COALESCE(delivery_phase,'') FROM mail_outbox WHERE id=$1`, id).Scan(&phase); err != nil {
			t.Error(err)
		}
		return errors.New("smtp down")
	})
	if err != nil || status != store.MailStatusPending || phase != "claimed" {
		t.Fatalf("status=%q phase=%q err=%v", status, phase, err)
	}
	var backoffOK bool
	if err := pool.QueryRowContext(ctx, `
		SELECT next_attempt_at - now() BETWEEN interval '14 minutes' AND interval '16 minutes'
		  FROM mail_outbox WHERE id=$1`, id).Scan(&backoffOK); err != nil || !backoffOK {
		t.Fatalf("backoff not on the database clock (ok=%v err=%v)", backoffOK, err)
	}
}
