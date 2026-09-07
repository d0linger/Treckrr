package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// The outbox exists so a failed send stops evaporating with its flash message.
// This walks the whole lifecycle against a real database: park, retry with
// backoff, deliver, and give up — with the audit trail as the visible record.
func TestMailOutboxLifecycleIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

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
	early, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, []byte) error {
		t.Fatal("a just-enqueued mail must not be attempted before its backoff")
		return nil
	})
	if err != nil || early != 0 {
		t.Fatalf("early process: delivered=%d err=%v", early, err)
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
	if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, []byte) error {
		return errors.New("smtp kaputt")
	}); err != nil {
		t.Fatalf("failing process: %v", err)
	}
	var attempts int
	var status, lastErr string
	var next time.Time
	if err := pool.QueryRowContext(ctx,
		`SELECT attempts, status, last_error, next_attempt_at FROM mail_outbox WHERE recipient=$1`, marker).
		Scan(&attempts, &status, &lastErr, &next); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if attempts != 1 || status != "pending" || lastErr != "smtp kaputt" {
		t.Errorf("after one failure: attempts=%d status=%s err=%q, want 1/pending/smtp kaputt", attempts, status, lastErr)
	}
	if !next.After(time.Now().Add(10 * time.Minute)) {
		t.Errorf("next attempt %v is not backed off", next)
	}

	// Successful delivery: sent, payload arrives intact, audit line written.
	due()
	var gotBody string
	var gotAtt []byte
	delivered, _, err := st.ProcessMailOutbox(ctx, func(_ context.Context, to, subject, body, attName, attType string, attData []byte) error {
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
	}); err != nil {
		t.Fatalf("enqueue 2: %v", err)
	}
	for range 6 {
		due()
		if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, []byte) error {
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
	if _, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, []byte) error {
		t.Fatal("a failed-for-good mail must never be retried")
		return nil
	}); err != nil {
		t.Fatalf("post-exhaust process: %v", err)
	}

	// Purge removes only delivered rows; the failed one is the record.
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
}
