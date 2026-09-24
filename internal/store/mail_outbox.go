package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/mail"
)

const (
	MailStatusPending   = "pending"
	MailStatusSending   = "sending"
	MailStatusSent      = "sent"
	MailStatusFailed    = "failed"
	MailStatusAmbiguous = "ambiguous"
	mailQueueMaxRows    = 1_000
	mailQueueMaxBytes   = 256 << 20
	mailQueueLockKey    = int64(472019260913)
)

// ErrMailQueueCapacity means the durable queue has reached its bounded row or
// attachment-byte budget and must be drained/redacted before accepting more.
var ErrMailQueueCapacity = errors.New("mail outbox capacity exceeded")

// OutboxMail is one durable outbound-mail intent.
type OutboxMail struct {
	ID            int64
	Kind          string
	NeighborID    int64
	BillingYearID int64
	Recipient     string
	Subject       string
	Body          string
	AttName       string
	AttType       string
	AttData       []byte
	Attempts      int
	Status        string
	DeliveryKey   string
	MessageID     string
	ClaimedAt     time.Time
	TerminalAt    time.Time
	// RetryFailed is a request-only control: an explicit single-recipient resend
	// may reopen a definitively failed intent. Batch callers leave it false.
	RetryFailed bool
	// Meta carries what the retry loop needs to finish the kind's bookkeeping
	// on delivery — for a Mahnung: stage, fee, grace and invoice number, so
	// RecordDunningNotice can be written when the mail ACTUALLY went out, not
	// merely when it was parked. Stored as JSONB (0052).
	Meta OutboxMeta
}

// OutboxMeta is the per-kind bookkeeping payload (see OutboxMail.Meta).
type OutboxMeta struct {
	Stage         int             `json:"stage,omitempty"`
	Fee           decimal.Decimal `json:"fee,omitempty"`
	GraceUntil    time.Time       `json:"grace_until,omitempty"`
	InvoiceNumber string          `json:"invoice_number,omitempty"`
}

// outboxMaxAttempts is how often a parked mail is retried before it is marked
// failed for good. With the backoff below the last attempt happens roughly a
// day after the first failure — long enough to ride out an SMTP outage, short
// enough that "the neighbor never got the invoice" surfaces the next day, not
// next month.
const outboxMaxAttempts = 6

// outboxBackoff returns how long to wait before attempt n (1-based).
// 15m, 30m, 1h, 2h, 4h, 8h — roughly doubling.
func outboxBackoff(attempt int) time.Duration {
	d := 15 * time.Minute
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	if d > 8*time.Hour {
		d = 8 * time.Hour
	}
	return d
}

// nullable turns id 0 ("no linked record") into NULL for foreign-key columns —
// id 0 never exists, and inserting it would violate the constraint.
func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// defaultDeliveryKey derives a stable idempotency key from logical content.
func defaultDeliveryKey(m OutboxMail, meta []byte) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s\x00%s",
		m.Kind, m.NeighborID, m.BillingYearID, strings.ToLower(strings.TrimSpace(m.Recipient)), m.MessageID, meta)))
	return fmt.Sprintf("mail:%x", digest[:])
}

// defaultMessageID derives the retry-stable SMTP identity for one intent.
func defaultMessageID(m OutboxMail) string {
	var atts []mail.Attachment
	if m.AttName != "" || len(m.AttData) > 0 {
		atts = []mail.Attachment{{Filename: m.AttName, ContentType: m.AttType, Data: m.AttData}}
	}
	return mail.StableMessageID("", m.Recipient, m.Subject, m.Body, atts)
}

// CreateMailIntent persists a message before SMTP is attempted. DeliveryKey is
// unique: a repeated request returns the existing intent and never creates a
// second independently deliverable row. An explicit RetryFailed request reopens
// only a definitively failed intent; sent and ambiguous outcomes stay terminal.
func (s *Store) CreateMailIntent(ctx context.Context, m OutboxMail) (OutboxMail, bool, error) {
	if m.Meta.Fee.IsNegative() {
		return OutboxMail{}, false, ErrNegativeDunningFee
	}
	meta, err := json.Marshal(m.Meta)
	if err != nil {
		return OutboxMail{}, false, fmt.Errorf("marshal outbox metadata: %w", err)
	}
	if strings.TrimSpace(m.MessageID) == "" {
		m.MessageID = defaultMessageID(m)
	}
	if strings.TrimSpace(m.DeliveryKey) == "" {
		m.DeliveryKey = defaultDeliveryKey(m, meta)
	}
	requestedMessageID := m.MessageID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OutboxMail{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	if m.NeighborID != 0 {
		if err := lockPersonalDataNeighbor(ctx, tx, m.NeighborID); err != nil {
			return OutboxMail{}, false, err
		}
	}
	// Serialize admission across replicas so two concurrent inserts cannot both
	// observe the last free slot/byte budget. Return an existing idempotent intent
	// before applying capacity limits: repeated requests add no storage.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, mailQueueLockKey); err != nil {
		return OutboxMail{}, false, err
	}
	err = tx.QueryRowContext(ctx, `
		SELECT id, status, attempts, message_id
		  FROM mail_outbox WHERE delivery_key=$1`, m.DeliveryKey).
		Scan(&m.ID, &m.Status, &m.Attempts, &m.MessageID)
	if err == nil {
		if m.RetryFailed && m.Status == MailStatusFailed {
			var queueRows, payloadBytes int64
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*), COALESCE(sum(octet_length(att_data)),0)
				  FROM mail_outbox
				 WHERE id<>$1
				   AND status IN ('pending','sending','failed','ambiguous')
				   AND redacted_at IS NULL`, m.ID).Scan(&queueRows, &payloadBytes); err != nil {
				return OutboxMail{}, false, err
			}
			if queueRows >= mailQueueMaxRows || int64(len(m.AttData)) > mailQueueMaxBytes-payloadBytes {
				return OutboxMail{}, false, fmt.Errorf("%w: rows=%d, attachment_bytes=%d",
					ErrMailQueueCapacity, queueRows, payloadBytes)
			}
			m.MessageID = requestedMessageID
			err := tx.QueryRowContext(ctx, `
				UPDATE mail_outbox
				   SET recipient=$2, subject=$3, body=$4, att_name=$5, att_type=$6,
				       att_data=$7, meta=$8, message_id=$9, status='pending', attempts=0,
				       next_attempt_at=now(), claimed_at=NULL, terminal_at=NULL,
				       sent_at=NULL, redacted_at=NULL, last_error=''
				 WHERE id=$1 AND status='failed'
				 RETURNING status, attempts`, m.ID, m.Recipient, m.Subject, m.Body,
				m.AttName, m.AttType, m.AttData, meta, m.MessageID).
				Scan(&m.Status, &m.Attempts)
			if err != nil {
				return OutboxMail{}, false, err
			}
			if err := tx.Commit(); err != nil {
				return OutboxMail{}, false, err
			}
			return m, true, nil
		}
		if err := tx.Commit(); err != nil {
			return OutboxMail{}, false, err
		}
		return m, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return OutboxMail{}, false, err
	}
	var queueRows, payloadBytes int64
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*), COALESCE(sum(octet_length(att_data)),0)
		  FROM mail_outbox
		 WHERE status IN ('pending','sending','failed','ambiguous')
		   AND redacted_at IS NULL`).Scan(&queueRows, &payloadBytes); err != nil {
		return OutboxMail{}, false, err
	}
	if queueRows >= mailQueueMaxRows || int64(len(m.AttData)) > mailQueueMaxBytes-payloadBytes {
		return OutboxMail{}, false, fmt.Errorf("%w: rows=%d, attachment_bytes=%d",
			ErrMailQueueCapacity, queueRows, payloadBytes)
	}
	created := true
	var next time.Time
	err = tx.QueryRowContext(ctx, `
		INSERT INTO mail_outbox
		       (kind, neighbor_id, billing_year_id, recipient, subject, body,
		        att_name, att_type, att_data, next_attempt_at, meta, delivery_key, message_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (delivery_key) WHERE delivery_key IS NOT NULL DO NOTHING
		RETURNING id, status, attempts, next_attempt_at`,
		m.Kind, nullable(m.NeighborID), nullable(m.BillingYearID), m.Recipient, m.Subject, m.Body,
		m.AttName, m.AttType, m.AttData, time.Now().Add(outboxBackoff(1)), meta,
		m.DeliveryKey, m.MessageID).Scan(&m.ID, &m.Status, &m.Attempts, &next)
	if errors.Is(err, sql.ErrNoRows) {
		created = false
		err = tx.QueryRowContext(ctx, `
			SELECT id, status, attempts, message_id
			  FROM mail_outbox WHERE delivery_key=$1`, m.DeliveryKey).
			Scan(&m.ID, &m.Status, &m.Attempts, &m.MessageID)
	}
	if err != nil {
		return OutboxMail{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return OutboxMail{}, false, err
	}
	return m, created, nil
}

// EnqueueMail preserves the existing queue API for callers that do not need to
// attempt immediately. Equivalent messages are idempotently coalesced.
func (s *Store) EnqueueMail(ctx context.Context, m OutboxMail) error {
	_, _, err := s.CreateMailIntent(ctx, m)
	return err
}

// PendingMailCount reports how many mails are parked (for /metrics).
func (s *Store) PendingMailCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM mail_outbox WHERE status IN ('pending','sending')`).Scan(&n)
	return n, err
}

// perMailBudget bounds ONE outbox delivery: the SMTP dial+dialog budget
// (10s+18s in mail.Send) plus the bookkeeping writes. Detached from the tick's
// context on purpose — the old shared 1-minute tick budget could expire
// between a successful SMTP dialog and the status='sent' UPDATE, leaving the
// row pending and the neighbor with a duplicate invoice on every later tick.
const perMailBudget = 45 * time.Second
