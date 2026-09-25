package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/store"
)

// The outbox exists so a failed send stops evaporating with its flash message.
// This walks the whole lifecycle against a real database: park, retry with
// backoff, deliver, and give up — with the audit trail as the visible record.
// TestMailOutboxLifecycleIntegration exercises durable parking, bounded retry,
// delivery bookkeeping, and terminal exhaustion against PostgreSQL.
func TestMailOutboxLifecycleIntegration(t *testing.T) {
	ctx := context.Background()
	// Processing and purging deliberately scan the whole queue. Keep this
	// lifecycle isolated rather than delivering another test's pending mail.
	st, pool := scratchStore(t)

	marker := fmt.Sprintf("outbox-it-%d@example.invalid", os.Getpid())
	purge := func() {
		if _, err := pool.ExecContext(ctx, `DELETE FROM mail_outbox WHERE recipient=$1`, marker); err != nil {
			t.Fatalf("purge outbox: %v", err)
		}
	}
	purge()
	t.Cleanup(purge)

	if err := st.EnqueueMail(ctx, store.OutboxMail{
		Kind: "beleg", Recipient: marker, Subject: "IT-Betreff", Body: "IT-Text",
		AttName: "r.pdf", AttType: "application/pdf", AttData: []byte("pdfbytes"),
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if n, _ := st.PendingMailCount(ctx); n < 1 {
		t.Fatalf("pending count %d, want >= 1", n)
	}

	// Freshly parked mail is NOT due yet — the first retry waits out the backoff,
	// so a still-broken SMTP server is not hammered seconds after failing.
	early, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		t.Fatal("a just-enqueued mail must not be attempted before its backoff")
		return nil
	})
	if err != nil || early != 0 {
		t.Fatalf("early process: delivered=%d err=%v", early, err)
	}
	// Simulate an intent created before migration 0059. Its first claim must
	// derive and persist a stable Message-ID before invoking the sender.
	if _, err := pool.ExecContext(ctx,
		`UPDATE mail_outbox SET message_id=NULL WHERE recipient=$1`, marker); err != nil {
		t.Fatalf("clear legacy message id: %v", err)
	}

	due := func() {
		if _, err := pool.ExecContext(ctx,
			`UPDATE mail_outbox SET next_attempt_at=now() WHERE recipient=$1 AND status='pending'`, marker); err != nil {
			t.Fatalf("force due: %v", err)
		}
	}

	// One failing attempt: stays pending, attempt counted, error recorded,
	// next attempt pushed into the future.
	due()
	var stableMessageID string
	if _, _, err := st.ProcessMailOutbox(ctx, func(_ context.Context, messageID, _, _, _, _, _ string, _ []byte) error {
		stableMessageID = messageID
		return errors.New("smtp kaputt")
	}); err != nil {
		t.Fatalf("failing process: %v", err)
	}
	var attempts int
	var status, lastErr, persistedMessageID string
	var next time.Time
	if err := pool.QueryRowContext(ctx,
		`SELECT attempts, status, last_error, next_attempt_at, message_id FROM mail_outbox WHERE recipient=$1`, marker).
		Scan(&attempts, &status, &lastErr, &next, &persistedMessageID); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if attempts != 1 || status != "pending" || lastErr != "smtp kaputt" {
		t.Errorf("after one failure: attempts=%d status=%s err=%q, want 1/pending/smtp kaputt", attempts, status, lastErr)
	}
	if !next.After(time.Now().Add(10 * time.Minute)) {
		t.Errorf("next attempt %v is not backed off", next)
	}
	if stableMessageID == "" || persistedMessageID != stableMessageID {
		t.Errorf("legacy Message-ID was not persisted: sent=%q stored=%q", stableMessageID, persistedMessageID)
	}

	// Successful delivery: sent, payload arrives intact, audit line written.
	due()
	var gotBody string
	var gotAtt []byte
	delivered, _, err := st.ProcessMailOutbox(ctx, func(_ context.Context, messageID, to, subject, body, attName, attType string, attData []byte) error {
		if messageID != stableMessageID {
			t.Fatalf("retry Message-ID = %q, want persisted %q", messageID, stableMessageID)
		}
		gotBody, gotAtt = body, attData
		return nil
	})
	if err != nil || delivered != 1 {
		t.Fatalf("deliver: n=%d err=%v", delivered, err)
	}
	if gotBody != "IT-Text" || string(gotAtt) != "pdfbytes" {
		t.Errorf("payload mangled in the queue: body=%q att=%q", gotBody, gotAtt)
	}
	var has bool
	if err := pool.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM audit_log WHERE action='mail_retry_sent' AND detail LIKE '%'||$1||'%')`,
		marker).Scan(&has); err != nil || !has {
		t.Errorf("no mail_retry_sent audit line (err=%v)", err)
	}

	// Exhaustion: a mail that keeps failing flips to failed with an audit line,
	// and is never attempted again.
	if err := st.EnqueueMail(ctx, store.OutboxMail{
		Kind: "mahnung", Recipient: marker, Subject: "IT-Mahnung", Body: "x",
		AttName: "mahnung.pdf", AttType: "application/pdf", AttData: []byte("sensitive-pdf"),
	}); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	for range 6 {
		due()
		if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
			return errors.New("dauerhaft kaputt")
		}); err != nil {
			t.Fatalf("exhaust: %v", err)
		}
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT status, attempts FROM mail_outbox WHERE recipient=$1 AND subject='IT-Mahnung'`, marker).
		Scan(&status, &attempts); err != nil {
		t.Fatalf("read exhausted: %v", err)
	}
	if status != "failed" || attempts != 6 {
		t.Errorf("exhausted mail: status=%s attempts=%d, want failed/6", status, attempts)
	}
	due() // even if forced due, a failed row must stay untouched
	if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		t.Fatal("a failed-for-good mail must never be retried")
		return nil
	}); err != nil {
		t.Fatalf("post-exhaust process: %v", err)
	}

	// Retention deletes delivered rows and redacts payload bytes from the failed
	// row while preserving its minimal delivery/failure ledger.
	if err := st.PurgeSentMail(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("purge sent: %v", err)
	}
	var sentLeft, failedLeft int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FILTER (WHERE status='sent'), count(*) FILTER (WHERE status='failed')
		   FROM mail_outbox WHERE recipient=$1`, marker).Scan(&sentLeft, &failedLeft); err != nil {
		t.Fatalf("count: %v", err)
	}
	if sentLeft != 0 || failedLeft != 1 {
		t.Errorf("after purge: sent=%d failed=%d, want 0/1", sentLeft, failedLeft)
	}
	var recipient, subject, body, attName, attType, retainedErr string
	var attData []byte
	var redacted bool
	if err := pool.QueryRowContext(ctx, `
		SELECT recipient, subject, body, att_name, att_type, COALESCE(att_data,''::bytea),
		       last_error, redacted_at IS NOT NULL
		  FROM mail_outbox WHERE recipient=$1 AND status='failed'`, marker).
		Scan(&recipient, &subject, &body, &attName, &attType, &attData, &retainedErr, &redacted); err != nil {
		t.Fatalf("read redacted row: %v", err)
	}
	if recipient != marker || subject != "IT-Mahnung" || retainedErr == "" {
		t.Errorf("minimal failure ledger was not retained: recipient=%q subject=%q error=%q", recipient, subject, retainedErr)
	}
	if body != "" || attName != "" || attType != "" || len(attData) != 0 || !redacted {
		t.Errorf("payload was not redacted: body=%q name=%q type=%q bytes=%d redacted=%v",
			body, attName, attType, len(attData), redacted)
	}
}

// TestMailOutboxMahnungRetryBookkeepingIntegration: a Mahnung delivered on
// RETRY must land in the Mahnhistorie (dunning_notices) and the send trail
// (beleg_sends) exactly like a synchronously delivered one — built from the
// meta the enqueue parked with the mail. A legacy row from before 0052 carries
// an empty meta and must NOT fabricate a Stage-0 notice.
func TestMailOutboxMahnungRetryBookkeepingIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)

	f := fixtures{Years: []int{2107}, NeighborNames: []string{"Outbox Mahnung 2107"}}
	purgeFixtures(t, ctx, pool, f)
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f) })

	baseID, err := st.CreateEmptyBase(ctx, 2107, "Outbox-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2107, baseID, "Outbox-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Outbox Mahnung 2107", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	marker := fmt.Sprintf("outbox-mahnung-it-%d@example.invalid", os.Getpid())
	purgeMail := func() {
		if _, err := pool.ExecContext(ctx, `DELETE FROM mail_outbox WHERE recipient=$1`, marker); err != nil {
			t.Fatalf("purge outbox: %v", err)
		}
	}
	purgeMail()
	t.Cleanup(purgeMail)

	grace := day(2107, 6, 20)
	if err := st.EnqueueMail(ctx, store.OutboxMail{
		Kind: "mahnung", NeighborID: nid, BillingYearID: yearID,
		Recipient: marker, Subject: "2. Mahnung · Rechnung IT-2107-001", Body: "b",
		Meta: store.OutboxMeta{Stage: 2, Fee: dec("15"), GraceUntil: grace, InvoiceNumber: "IT-2107-001"},
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`UPDATE mail_outbox SET next_attempt_at=now() WHERE recipient=$1`, marker); err != nil {
		t.Fatalf("force due: %v", err)
	}
	if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		return errors.New("temporary SMTP failure")
	}); err != nil {
		t.Fatalf("first failed attempt: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`UPDATE mail_outbox SET next_attempt_at=now() WHERE recipient=$1 AND status='pending'`, marker); err != nil {
		t.Fatalf("force retry due: %v", err)
	}
	// >= 1, not == 1: the outbox is a shared global queue and another test's
	// leftover due row would otherwise flake this count.
	delivered, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		return nil
	})
	if err != nil || delivered < 1 {
		t.Fatalf("process: delivered=%d err=%v", delivered, err)
	}

	var stage int
	var invoiceNo, channel, fee string
	var graceGot time.Time
	if err := pool.QueryRowContext(ctx, `
		SELECT stage, invoice_number, channel, fee::text, COALESCE(grace_until, '0001-01-01'::date)
		  FROM dunning_notices WHERE billing_year_id=$1 AND neighbor_id=$2`, yearID, nid).
		Scan(&stage, &invoiceNo, &channel, &fee, &graceGot); err != nil {
		t.Fatalf("read notice: %v", err)
	}
	if stage != 2 || invoiceNo != "IT-2107-001" || channel != "e-mail (Wiederholung)" {
		t.Errorf("notice = stage %d / %q / %q, want 2 / IT-2107-001 / e-mail (Wiederholung)", stage, invoiceNo, channel)
	}
	if dec(fee).Cmp(dec("15")) != 0 {
		t.Errorf("notice fee = %s, want 15", fee)
	}
	if graceGot.Format("2006-01-02") != "2107-06-20" {
		t.Errorf("notice grace = %s, want 2107-06-20", graceGot.Format("2006-01-02"))
	}
	var sends int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM beleg_sends WHERE billing_year_id=$1 AND neighbor_id=$2 AND channel='mahnung'`,
		yearID, nid).Scan(&sends); err != nil {
		t.Fatalf("read sends: %v", err)
	}
	if sends != 1 {
		t.Errorf("beleg_sends = %d, want 1", sends)
	}
	history, err := st.ListBelegSends(ctx, yearID, nid)
	if err != nil || len(history) != 1 {
		t.Fatalf("send history: len=%d err=%v", len(history), err)
	}
	if b := history[0]; b.ID == 0 || b.BillingYearID != yearID || b.NeighborID != nid || b.Channel != "mahnung" || b.SentAt.IsZero() {
		t.Errorf("send history must populate the complete model: %+v", b)
	}

	// Legacy row (pre-0052): empty meta must not fabricate a Stage-0 notice —
	// but the send trail is still written.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO mail_outbox (kind, neighbor_id, billing_year_id, recipient, subject, body,
		                         att_name, att_type, att_data, next_attempt_at)
		VALUES ('mahnung', $1, $2, $3, 'Alt-Mahnung', 'b', '', '', ''::bytea, now())`,
		nid, yearID, marker); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	delivered, _, err = st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, string, []byte) error {
		return nil
	})
	if err != nil || delivered < 1 {
		t.Fatalf("legacy process: delivered=%d err=%v", delivered, err)
	}
	var notices int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM dunning_notices WHERE billing_year_id=$1 AND neighbor_id=$2`, yearID, nid).
		Scan(&notices); err != nil {
		t.Fatalf("count notices: %v", err)
	}
	if notices != 1 {
		t.Errorf("after legacy delivery: %d notices, want still 1 (no fabricated Stage-0 row)", notices)
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM beleg_sends WHERE billing_year_id=$1 AND neighbor_id=$2 AND channel='mahnung'`,
		yearID, nid).Scan(&sends); err != nil {
		t.Fatalf("read sends: %v", err)
	}
	if sends != 2 {
		t.Errorf("beleg_sends after legacy delivery = %d, want 2", sends)
	}
}

// TestMailIntentIdempotencyAndAmbiguityIntegration verifies duplicate requests
// stay terminal unless a single-recipient action explicitly forces a resend.
func TestMailIntentIdempotencyAndAmbiguityIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)
	marker := fmt.Sprintf("intent-%d-%d@example.invalid", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := pool.ExecContext(ctx, `DELETE FROM mail_outbox WHERE recipient=$1`, marker); err != nil {
			t.Errorf("purge intent rows: %v", err)
		}
	})

	const workers = 8
	var created atomic.Int32
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, wasCreated, err := st.CreateMailIntent(ctx, store.OutboxMail{
				Kind: "beleg", Recipient: marker, Subject: "Idempotent", Body: "same",
				DeliveryKey: "it:idempotent:" + marker, MessageID: "<it-idempotent@example.invalid>",
			})
			if wasCreated {
				created.Add(1)
			}
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("create intent: %v", err)
		}
	}
	if got := created.Load(); got != 1 {
		t.Fatalf("created intents = %d, want 1", got)
	}
	var count int
	var id int64
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*), min(id) FROM mail_outbox WHERE delivery_key=$1`, "it:idempotent:"+marker).
		Scan(&count, &id); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("durable intents = %d, want 1", count)
	}

	status, err := st.AttemptMail(ctx, id,
		func(context.Context, string, string, string, string, string, string, []byte) error {
			return mail.Ambiguous(errors.New("final SMTP reply lost"))
		})
	if err != nil || status != store.MailStatusAmbiguous {
		t.Fatalf("ambiguous attempt: status=%q err=%v", status, err)
	}
	status, err = st.AttemptMail(ctx, id,
		func(context.Context, string, string, string, string, string, string, []byte) error {
			t.Fatal("ambiguous delivery must never be retried")
			return nil
		})
	if err != nil || status != store.MailStatusAmbiguous {
		t.Fatalf("terminal ambiguous intent: status=%q err=%v", status, err)
	}
	duplicate := store.OutboxMail{
		Kind: "beleg", Recipient: marker, Subject: "Idempotent", Body: "same",
		DeliveryKey: "it:idempotent:" + marker, MessageID: "<it-idempotent@example.invalid>",
		RetryFailed: true,
	}
	unchanged, wasCreated, err := st.CreateMailIntent(ctx, duplicate)
	if err != nil || wasCreated || unchanged.Status != store.MailStatusAmbiguous {
		t.Fatalf("failed-only retry reopened ambiguity: status=%q created=%v err=%v",
			unchanged.Status, wasCreated, err)
	}
	duplicate.ForceResend = true
	reopened, wasCreated, err := st.CreateMailIntent(ctx, duplicate)
	if err != nil || !wasCreated || reopened.Status != store.MailStatusPending || reopened.Attempts != 0 {
		t.Fatalf("forced ambiguous resend: status=%q attempts=%d created=%v err=%v",
			reopened.Status, reopened.Attempts, wasCreated, err)
	}
}

// TestAttemptMailSettlesAfterCallerCancellationIntegration verifies a claimed
// SMTP attempt retains its bounded settlement context after the request ends.
func TestAttemptMailSettlesAfterCallerCancellationIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	cleanupCtx := context.Background()
	marker := fmt.Sprintf("attempt-cancel-%d-%d@example.invalid", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() {
		if _, err := pool.ExecContext(cleanupCtx, `DELETE FROM mail_outbox WHERE recipient=$1`, marker); err != nil {
			t.Errorf("purge canceled-attempt row: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	intent, created, err := st.CreateMailIntent(ctx, store.OutboxMail{
		Kind: "beleg", Recipient: marker, Subject: "Cancellation", Body: "body",
		DeliveryKey: "it:attempt-cancel:" + marker,
	})
	if err != nil || !created {
		t.Fatalf("create intent: created=%v err=%v", created, err)
	}
	status, err := st.AttemptMail(ctx, intent.ID,
		func(sendCtx context.Context, _, _, _, _, _, _ string, _ []byte) error {
			cancel()
			if err := sendCtx.Err(); err != nil {
				t.Fatalf("caller cancellation reached claimed attempt: %v", err)
			}
			return nil
		})
	if err != nil || status != store.MailStatusSent {
		t.Fatalf("attempt settlement: status=%q err=%v", status, err)
	}
	var stored string
	if err := pool.QueryRowContext(cleanupCtx, `SELECT status FROM mail_outbox WHERE id=$1`, intent.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != store.MailStatusSent {
		t.Fatalf("stored status = %q, want sent", stored)
	}
}

// TestExplicitMailRetryReopensOnlyFailedIntentIntegration verifies that batch-
// style duplicates stay terminal while an explicit retry refreshes redacted data.
func TestExplicitMailRetryReopensOnlyFailedIntentIntegration(t *testing.T) {
	ctx := context.Background()
	st, pool := scratchStore(t)
	marker := fmt.Sprintf("explicit-retry-%d-%d@example.invalid", os.Getpid(), time.Now().UnixNano())
	key := "it:explicit-retry:" + marker
	t.Cleanup(func() {
		if _, err := pool.ExecContext(ctx, `DELETE FROM mail_outbox WHERE recipient=$1`, marker); err != nil {
			t.Errorf("purge explicit retry row: %v", err)
		}
	})

	base := store.OutboxMail{
		Kind: "beleg", Recipient: marker, Subject: "Original", Body: "old",
		AttName: "old.pdf", AttType: "application/pdf", AttData: []byte("old-pdf"),
		DeliveryKey: key, MessageID: "<old@example.invalid>",
	}
	intent, created, err := st.CreateMailIntent(ctx, base)
	if err != nil || !created {
		t.Fatalf("create failed intent: created=%v err=%v", created, err)
	}
	if _, err := pool.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET status='failed', attempts=6, terminal_at=now(), redacted_at=now(),
		       body='', att_name='', att_type='', att_data=NULL
		 WHERE id=$1`, intent.ID); err != nil {
		t.Fatal(err)
	}

	unchanged, created, err := st.CreateMailIntent(ctx, base)
	if err != nil || created || unchanged.Status != store.MailStatusFailed {
		t.Fatalf("ordinary duplicate reopened failure: status=%q created=%v err=%v", unchanged.Status, created, err)
	}

	base.RetryFailed = true
	base.Subject = "Explicit retry"
	base.Body = "fresh"
	base.AttName = "fresh.pdf"
	base.AttData = []byte("fresh-pdf")
	base.MessageID = "<fresh@example.invalid>"
	reopened, created, err := st.CreateMailIntent(ctx, base)
	if err != nil || !created || reopened.Status != store.MailStatusPending || reopened.Attempts != 0 {
		t.Fatalf("explicit retry: status=%q attempts=%d created=%v err=%v",
			reopened.Status, reopened.Attempts, created, err)
	}
	var subject, body, attName, messageID string
	var attData []byte
	var redacted bool
	if err := pool.QueryRowContext(ctx, `
		SELECT subject, body, att_name, COALESCE(att_data,''::bytea), message_id,
		       redacted_at IS NOT NULL
		  FROM mail_outbox WHERE id=$1`, intent.ID).
		Scan(&subject, &body, &attName, &attData, &messageID, &redacted); err != nil {
		t.Fatal(err)
	}
	if subject != base.Subject || body != base.Body || attName != base.AttName ||
		string(attData) != string(base.AttData) || messageID != base.MessageID || redacted {
		t.Fatalf("reopened payload not refreshed: subject=%q body=%q name=%q bytes=%q id=%q redacted=%v",
			subject, body, attName, attData, messageID, redacted)
	}
}
