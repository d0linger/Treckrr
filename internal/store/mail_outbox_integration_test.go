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

// TestMailOutboxMahnungRetryBookkeepingIntegration: a Mahnung delivered on
// RETRY must land in the Mahnhistorie (dunning_notices) and the send trail
// (beleg_sends) exactly like a synchronously delivered one — built from the
// meta the enqueue parked with the mail. A legacy row from before 0052 carries
// an empty meta and must NOT fabricate a Stage-0 notice.
func TestMailOutboxMahnungRetryBookkeepingIntegration(t *testing.T) {
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
	// >= 1, not == 1: the outbox is a shared global queue and another test's
	// leftover due row would otherwise flake this count.
	delivered, _, err := st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, []byte) error {
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

	// Legacy row (pre-0052): empty meta must not fabricate a Stage-0 notice —
	// but the send trail is still written.
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO mail_outbox (kind, neighbor_id, billing_year_id, recipient, subject, body,
		                         att_name, att_type, att_data, next_attempt_at)
		VALUES ('mahnung', $1, $2, $3, 'Alt-Mahnung', 'b', '', '', ''::bytea, now())`,
		nid, yearID, marker); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	delivered, _, err = st.ProcessMailOutbox(ctx, func(context.Context, string, string, string, string, string, []byte) error {
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
