package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/mail"
)

// rowScanner accepts both database rows and single-row query results.
type rowScanner interface {
	Scan(dest ...any) error
}

const outboxSelectColumns = `id, kind, COALESCE(neighbor_id,0), COALESCE(billing_year_id,0),
	recipient, subject, body, att_name, att_type, COALESCE(att_data,''::bytea), attempts,
	status, COALESCE(delivery_key,''), COALESCE(message_id,''), claimed_at, terminal_at,
	COALESCE(meta,'{}'::jsonb)`

// MailSender performs one SMTP attempt using the durable Message-ID stored with
// the intent.
type MailSender func(context.Context, string, string, string, string, string, string, []byte) error

// scanOutboxMail decodes one durable delivery intent and its nullable clocks.
func scanOutboxMail(row rowScanner) (OutboxMail, error) {
	var (
		m                   OutboxMail
		meta                []byte
		claimedAt, terminal sql.NullTime
	)
	err := row.Scan(&m.ID, &m.Kind, &m.NeighborID, &m.BillingYearID, &m.Recipient,
		&m.Subject, &m.Body, &m.AttName, &m.AttType, &m.AttData, &m.Attempts,
		&m.Status, &m.DeliveryKey, &m.MessageID, &claimedAt, &terminal, &meta)
	if err != nil {
		return OutboxMail{}, err
	}
	if claimedAt.Valid {
		m.ClaimedAt = claimedAt.Time
	}
	if terminal.Valid {
		m.TerminalAt = terminal.Time
	}
	if err := json.Unmarshal(meta, &m.Meta); err != nil {
		return OutboxMail{}, fmt.Errorf("decode outbox metadata for %d: %w", m.ID, err)
	}
	// Rows created before migration 0059 have no stored Message-ID. Derive the
	// same content identity on every read; the claim below persists it before
	// SMTP so legacy pending deliveries remain sendable and retry-stable.
	if strings.TrimSpace(m.MessageID) == "" {
		m.MessageID = defaultMessageID(m)
	}
	return m, nil
}

// AttemptMail immediately attempts a newly created durable intent. Callers must
// use CreateMailIntent's created result as the gate so duplicate requests cannot
// bypass retry backoff.
func (s *Store) AttemptMail(ctx context.Context, id int64,
	send MailSender,
) (string, error) {
	m, err := scanOutboxMail(s.db.QueryRowContext(ctx,
		`SELECT `+outboxSelectColumns+` FROM mail_outbox WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	mctx, cancel := context.WithTimeout(ctx, perMailBudget)
	defer cancel()
	status, _, err := s.processOneOutboxMail(mctx, m, false, send)
	return status, err
}

// ProcessMailOutbox delivers due pending intents. Ambiguous deliveries are
// terminal and deliberately never selected for retry.
func (s *Store) ProcessMailOutbox(ctx context.Context,
	send MailSender,
) (delivered, exhausted int, err error) {
	// A process can die after transmitting DATA but before settling the row.
	// Once two per-message budgets have elapsed, retrying that claim is unsafe.
	if _, err := s.db.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET status='ambiguous', terminal_at=now(),
		       last_error=CASE WHEN last_error='' THEN 'worker stopped during delivery' ELSE last_error END
		 WHERE status='sending' AND claimed_at < $1`, time.Now().Add(-2*perMailBudget)); err != nil {
		return 0, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+outboxSelectColumns+`
		  FROM mail_outbox
		 WHERE status='pending' AND next_attempt_at <= now()
		 ORDER BY id
		 LIMIT 10`)
	if err != nil {
		return 0, 0, err
	}
	var due []OutboxMail
	for rows.Next() {
		m, scanErr := scanOutboxMail(rows)
		if scanErr != nil {
			_ = rows.Close()
			return 0, 0, scanErr
		}
		due = append(due, m)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, m := range due {
		if ctx.Err() != nil {
			return delivered, exhausted, nil
		}
		mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), perMailBudget)
		status, attempted, oneErr := s.processOneOutboxMail(mctx, m, true, send)
		cancel()
		if oneErr != nil {
			return delivered, exhausted, oneErr
		}
		if !attempted {
			continue
		}
		switch status {
		case MailStatusSent:
			delivered++
		case MailStatusFailed, MailStatusAmbiguous:
			exhausted++
		}
	}
	return delivered, exhausted, nil
}

// processOneOutboxMail atomically claims one intent, invokes its transport once,
// and settles the durable outcome without automatically retrying ambiguity.
func (s *Store) processOneOutboxMail(ctx context.Context, m OutboxMail, dueOnly bool,
	send MailSender,
) (status string, attempted bool, err error) {
	var attempts int
	err = s.db.QueryRowContext(ctx, `
		UPDATE mail_outbox
		   SET status='sending', claimed_at=now(), attempts=attempts+1, next_attempt_at=$2,
		       message_id=COALESCE(NULLIF(message_id,''),$4)
		 WHERE id=$1 AND status='pending' AND ($3 = false OR next_attempt_at <= now())
		 RETURNING attempts`, m.ID, time.Now().Add(outboxBackoff(m.Attempts+1)), dueOnly, m.MessageID).
		Scan(&attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return m.Status, false, nil
	}
	if err != nil {
		return "", false, err
	}

	sendErr := send(ctx, m.MessageID, m.Recipient, m.Subject, m.Body, m.AttName, m.AttType, m.AttData)
	if sendErr == nil {
		if err := s.settleDelivered(ctx, m, attempts); err != nil {
			detail := "delivery confirmed but bookkeeping failed: " + safeMailError(err)
			markedStatus, markErr := s.markMailAmbiguous(ctx, m, detail)
			if markErr != nil {
				return MailStatusSending, true, fmt.Errorf("settle delivered mail: %w", errors.Join(err, markErr))
			}
			return markedStatus, true, nil
		}
		return MailStatusSent, true, nil
	}
	if mail.IsAmbiguous(sendErr) {
		markedStatus, err := s.markMailAmbiguous(ctx, m, safeMailError(sendErr))
		if err != nil {
			return MailStatusSending, true, err
		}
		return markedStatus, true, nil
	}
	return s.settleRejected(ctx, m, attempts, sendErr)
}

// settleDelivered commits the sent state, business history, and audit atomically.
func (s *Store) settleDelivered(ctx context.Context, m OutboxMail, attempts int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET status='sent', sent_at=now(), terminal_at=now(), claimed_at=NULL, last_error=''
		 WHERE id=$1 AND status='sending'`, m.ID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("delivery intent %d was not in sending state", m.ID)
	}

	channel := "e-mail"
	if attempts > 1 {
		channel = "e-mail (Wiederholung)"
	}
	if m.Kind == "mahnung" && m.NeighborID != 0 && m.BillingYearID != 0 && m.Meta.InvoiceNumber != "" {
		var grace any
		if !m.Meta.GraceUntil.IsZero() {
			grace = m.Meta.GraceUntil
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO dunning_notices
			       (billing_year_id, neighbor_id, invoice_number, stage, channel, grace_until, fee)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			m.BillingYearID, m.NeighborID, m.Meta.InvoiceNumber, m.Meta.Stage, channel, grace, m.Meta.Fee); err != nil {
			return err
		}
	}
	if m.NeighborID != 0 && m.BillingYearID != 0 && (m.Kind == "beleg" || m.Kind == "mahnung") {
		sendChannel := "e-mail"
		if m.Kind == "mahnung" {
			sendChannel = "mahnung"
		} else if attempts > 1 {
			sendChannel = "e-mail (Wiederholung)"
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO beleg_sends (billing_year_id, neighbor_id, channel) VALUES ($1,$2,$3)`,
			m.BillingYearID, m.NeighborID, sendChannel); err != nil {
			return err
		}
	}
	action := m.Kind + "_email"
	if attempts > 1 {
		action = "mail_retry_sent"
	}
	if err := addMailAuditTx(ctx, tx, action, m, fmt.Sprintf(
		"%s nach %d Versuch(en) zugestellt an %s", m.Subject, attempts, m.Recipient)); err != nil {
		return err
	}
	return tx.Commit()
}

// settleRejected either schedules a bounded retry or records terminal failure.
func (s *Store) settleRejected(ctx context.Context, m OutboxMail, attempts int, sendErr error) (string, bool, error) {
	status := MailStatusPending
	terminal := any(nil)
	if attempts >= outboxMaxAttempts {
		status = MailStatusFailed
		terminal = time.Now()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", true, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET status=$2, claimed_at=NULL, terminal_at=$3, last_error=$4
		 WHERE id=$1 AND status='sending'`, m.ID, status, terminal, safeMailError(sendErr)); err != nil {
		return "", true, err
	}
	if status == MailStatusFailed {
		if err := addMailAuditTx(ctx, tx, "mail_retry_failed", m, fmt.Sprintf(
			"%s endgültig NICHT zugestellt an %s (%d Versuche): %s",
			m.Subject, m.Recipient, attempts, safeMailError(sendErr))); err != nil {
			return "", true, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", true, err
	}
	return status, true, nil
}

// markMailAmbiguous parks an uncertain SMTP outcome for manual reconciliation.
func (s *Store) markMailAmbiguous(ctx context.Context, m OutboxMail, detail string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	var status string
	err = tx.QueryRowContext(ctx, `
		UPDATE mail_outbox
		   SET status='ambiguous', terminal_at=now(), claimed_at=NULL, last_error=$2
		 WHERE id=$1 AND status='sending'
		 RETURNING status`, m.ID, detail).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRowContext(ctx, `SELECT status FROM mail_outbox WHERE id=$1`, m.ID).Scan(&status); err != nil {
			return "", err
		}
		return status, nil
	}
	if err != nil {
		return "", err
	}
	if err := addMailAuditTx(ctx, tx, "mail_delivery_ambiguous", m,
		m.Subject+" · Zustellung unklar; keine automatische Wiederholung · "+detail); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return MailStatusAmbiguous, nil
}

// addMailAuditTx appends delivery evidence within the settlement transaction.
func addMailAuditTx(ctx context.Context, tx *sql.Tx, action string, m OutboxMail, detail string) error {
	actor, _ := ctx.Value(auditActorKey{}).(AuditActor)
	if actor.Username == "" {
		ctx = WithAuditActor(ctx, AuditActor{Username: "system"})
	}
	return addAuditTx(ctx, tx, action, m.Kind, m.DeliveryKey, detail)
}

// safeMailError flattens control characters and bounds persisted error detail.
func safeMailError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(err.Error())
	value = strings.ToValidUTF8(value, "�")
	runes := []rune(value)
	if len(runes) > 1000 {
		value = string(runes[:1000])
	}
	return value
}

// PurgeSentMail removes delivered rows and redacts message bodies and attachment
// bytes from terminal failed/ambiguous rows older than the same approved
// retention cutoff. Recipient, subject, timestamps, error, delivery identity,
// and structured document metadata remain as the minimal failure ledger.
func (s *Store) PurgeSentMail(ctx context.Context, olderThan time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM mail_outbox WHERE status='sent' AND sent_at < $1`, olderThan); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET body='', att_name='', att_type='', att_data=NULL, redacted_at=now()
		 WHERE status IN ('failed','ambiguous')
		   AND redacted_at IS NULL
		   AND COALESCE(terminal_at, created_at) < $1`, olderThan); err != nil {
		return err
	}
	return tx.Commit()
}
