package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrMailPayloadUnavailable prevents resending a terminal intent whose retained
// body or attachment was already redacted by the privacy retention job.
var ErrMailPayloadUnavailable = errors.New("mail payload is no longer available")

// ErrMailStateConflict reports that an operator action no longer matches the
// current durable delivery state (for example, another worker already sent it).
var ErrMailStateConflict = errors.New("mail state no longer permits this action")

// MailOperationsStatus is the compact operational summary shared by the admin
// status strip and the full mail-outbox center.
type MailOperationsStatus struct {
	Pending          int
	Sending          int
	Sent             int
	Failed           int
	Ambiguous        int
	Held             int
	RecurringBlocked int
}

// NeedsAttention reports whether an operator must make a delivery decision.
func (s MailOperationsStatus) NeedsAttention() bool {
	return s.Failed+s.Ambiguous+s.Held+s.RecurringBlocked > 0
}

// ActiveMail reports mail currently waiting or being delivered.
func (s MailOperationsStatus) ActiveMail() int { return s.Pending + s.Sending }

// OperationsStatus aggregates existing durable state without mutating it.
func (s *Store) OperationsStatus(ctx context.Context) (MailOperationsStatus, error) {
	var out MailOperationsStatus
	err := s.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE status='pending'),
		       count(*) FILTER (WHERE status='sending'),
		       count(*) FILTER (WHERE status='sent'),
		       count(*) FILTER (WHERE status='failed'),
		       count(*) FILTER (WHERE status='ambiguous'),
		       count(*) FILTER (WHERE status='held'),
		       (SELECT count(*) FROM recurring_entries WHERE active AND last_error<>'')
		  FROM mail_outbox`).Scan(&out.Pending, &out.Sending, &out.Sent, &out.Failed,
		&out.Ambiguous, &out.Held, &out.RecurringBlocked)
	return out, err
}

// MailOutboxItem is the payload-free admin view of a delivery intent.
type MailOutboxItem struct {
	ID            int64
	Kind          string
	NeighborName  string
	BillingYear   int
	Recipient     string
	Subject       string
	Status        string
	Attempts      int
	CreatedAt     time.Time
	NextAttemptAt time.Time
	SentAt        *time.Time
	TerminalAt    *time.Time
	HeldAt        *time.Time
	LastError     string
	Redacted      bool
}

// StatusLabel renders the delivery state in concise German operator language.
func (m MailOutboxItem) StatusLabel() string {
	switch m.Status {
	case MailStatusPending:
		return "Wartend"
	case MailStatusSending:
		return "Wird gesendet"
	case MailStatusSent:
		return "Gesendet"
	case MailStatusFailed:
		return "Fehlgeschlagen"
	case MailStatusAmbiguous:
		return "Zustellung unklar"
	case MailStatusHeld:
		return "Angehalten"
	default:
		return m.Status
	}
}

// MailAttempt is one immutable delivery attempt shown in the intent detail.
type MailAttempt struct {
	AttemptNo   int
	StartedAt   time.Time
	DataStarted *time.Time
	FinishedAt  *time.Time
	Outcome     string
	Detail      string
}

// OutcomeLabel renders the attempt result without relying on color alone.
func (a MailAttempt) OutcomeLabel() string {
	switch a.Outcome {
	case "sending":
		return "Läuft"
	case "sent":
		return "Zugestellt"
	case "retry":
		return "Erneuter Versuch geplant"
	case "failed":
		return "Endgültig fehlgeschlagen"
	case "ambiguous":
		return "Zustellung unklar"
	default:
		return a.Outcome
	}
}

// MailOutboxDetail combines the payload-free intent metadata and its recorded
// attempts. Message bodies and attachment bytes never reach the admin template.
type MailOutboxDetail struct {
	Item     MailOutboxItem
	Attempts []MailAttempt
}

// validMailStatusFilter accepts only states represented by the database check.
func validMailStatusFilter(status string) bool {
	switch status {
	case "", MailStatusPending, MailStatusSending, MailStatusSent, MailStatusFailed,
		MailStatusAmbiguous, MailStatusHeld:
		return true
	default:
		return false
	}
}

// ListMailOutbox returns recent delivery intents for one optional exact status.
func (s *Store) ListMailOutbox(ctx context.Context, status string, limit int) ([]MailOutboxItem, error) {
	if !validMailStatusFilter(status) {
		return nil, fmt.Errorf("invalid mail status filter %q", status)
	}
	if limit <= 0 || limit > 250 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT o.id, o.kind, COALESCE(n.name,''), COALESCE(y.year,0), o.recipient,
		       o.subject, o.status, o.attempts, o.created_at, o.next_attempt_at,
		       o.sent_at, o.terminal_at, o.held_at, o.last_error,
		       o.redacted_at IS NOT NULL
		  FROM mail_outbox o
		  LEFT JOIN neighbors n ON n.id=o.neighbor_id
		  LEFT JOIN billing_years y ON y.id=o.billing_year_id
		 WHERE ($1='' OR o.status=$1)
		 ORDER BY CASE WHEN o.status IN ('ambiguous','failed','held') THEN 0
		               WHEN o.status IN ('pending','sending') THEN 1 ELSE 2 END,
		          o.id DESC
		 LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MailOutboxItem, 0)
	for rows.Next() {
		item, err := scanMailOutboxItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// GetMailOutboxDetail returns one intent and every attempt recorded since the
// attempt-history migration was deployed.
func (s *Store) GetMailOutboxDetail(ctx context.Context, id int64) (MailOutboxDetail, error) {
	item, err := scanMailOutboxItem(s.db.QueryRowContext(ctx, `
		SELECT o.id, o.kind, COALESCE(n.name,''), COALESCE(y.year,0), o.recipient,
		       o.subject, o.status, o.attempts, o.created_at, o.next_attempt_at,
		       o.sent_at, o.terminal_at, o.held_at, o.last_error,
		       o.redacted_at IS NOT NULL
		  FROM mail_outbox o
		  LEFT JOIN neighbors n ON n.id=o.neighbor_id
		  LEFT JOIN billing_years y ON y.id=o.billing_year_id
		 WHERE o.id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return MailOutboxDetail{}, ErrNotFound
	}
	if err != nil {
		return MailOutboxDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT attempt_no, started_at, data_started_at, finished_at, outcome, detail
		  FROM mail_outbox_attempts WHERE outbox_id=$1 ORDER BY attempt_no DESC`, id)
	if err != nil {
		return MailOutboxDetail{}, err
	}
	defer rows.Close()
	detail := MailOutboxDetail{Item: item, Attempts: make([]MailAttempt, 0)}
	for rows.Next() {
		var a MailAttempt
		var dataStarted, finished sql.NullTime
		if err := rows.Scan(&a.AttemptNo, &a.StartedAt, &dataStarted, &finished,
			&a.Outcome, &a.Detail); err != nil {
			return MailOutboxDetail{}, err
		}
		if dataStarted.Valid {
			a.DataStarted = &dataStarted.Time
		}
		if finished.Valid {
			a.FinishedAt = &finished.Time
		}
		detail.Attempts = append(detail.Attempts, a)
	}
	return detail, rows.Err()
}

// RequeueMail schedules an explicit retry of a failed intent. An ambiguous
// intent requires forceAmbiguous=true because resending can create a duplicate.
func (s *Store) RequeueMail(ctx context.Context, id int64, forceAmbiguous bool) (MailOutboxItem, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return MailOutboxItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var m OutboxMail
	var redacted bool
	err = tx.QueryRowContext(ctx, `
		SELECT kind, recipient, subject, status, COALESCE(delivery_key,''), redacted_at IS NOT NULL
		  FROM mail_outbox WHERE id=$1 FOR UPDATE`, id).
		Scan(&m.Kind, &m.Recipient, &m.Subject, &m.Status, &m.DeliveryKey, &redacted)
	if errors.Is(err, sql.ErrNoRows) {
		return MailOutboxItem{}, ErrNotFound
	}
	if err != nil {
		return MailOutboxItem{}, err
	}
	if redacted {
		return MailOutboxItem{}, ErrMailPayloadUnavailable
	}
	allowed := (!forceAmbiguous && m.Status == MailStatusFailed) ||
		(forceAmbiguous && m.Status == MailStatusAmbiguous)
	if !allowed {
		return MailOutboxItem{}, ErrMailStateConflict
	}
	previous := m.Status
	if _, err := tx.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET status='pending', attempts=0, next_attempt_at=now(), claimed_at=NULL,
		       terminal_at=NULL, sent_at=NULL, held_at=NULL, delivery_phase=NULL,
		       last_error=''
		 WHERE id=$1`, id); err != nil {
		return MailOutboxItem{}, err
	}
	m.ID = id
	action := "mail_manual_retry"
	detail := m.Subject + " · fehlgeschlagene Zustellung manuell erneut eingeplant"
	if previous == MailStatusAmbiguous {
		action = "mail_force_resend"
		detail = m.Subject + " · trotz unklarem Ausgang ausdrücklich erneut eingeplant"
	}
	if err := addMailAuditTx(ctx, tx, action, m, detail); err != nil {
		return MailOutboxItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return MailOutboxItem{}, err
	}
	return s.GetMailOutboxItem(ctx, id)
}

// GetMailOutboxItem reloads one payload-free admin row.
func (s *Store) GetMailOutboxItem(ctx context.Context, id int64) (MailOutboxItem, error) {
	detail, err := s.GetMailOutboxDetail(ctx, id)
	return detail.Item, err
}

// scanMailOutboxItem decodes the shared payload-free projection used by the
// list, detail and post-action reload queries.
func scanMailOutboxItem(row rowScanner) (MailOutboxItem, error) {
	var item MailOutboxItem
	var sentAt, terminalAt, heldAt sql.NullTime
	err := row.Scan(&item.ID, &item.Kind, &item.NeighborName, &item.BillingYear,
		&item.Recipient, &item.Subject, &item.Status, &item.Attempts, &item.CreatedAt,
		&item.NextAttemptAt, &sentAt, &terminalAt, &heldAt, &item.LastError, &item.Redacted)
	if sentAt.Valid {
		item.SentAt = &sentAt.Time
	}
	if terminalAt.Valid {
		item.TerminalAt = &terminalAt.Time
	}
	if heldAt.Valid {
		item.HeldAt = &heldAt.Time
	}
	return item, err
}
