package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// OutboxMail is one parked outbound mail awaiting retry.
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

// EnqueueMail parks a mail whose synchronous send failed, for retry by the
// maintenance loop.
func (s *Store) EnqueueMail(ctx context.Context, m OutboxMail) error {
	// 0 means "no linked record" and must become NULL — both columns carry
	// foreign keys, and id 0 never exists.
	nullable := func(id int64) any {
		if id == 0 {
			return nil
		}
		return id
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO mail_outbox (kind, neighbor_id, billing_year_id, recipient, subject, body,
		                          att_name, att_type, att_data, next_attempt_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		m.Kind, nullable(m.NeighborID), nullable(m.BillingYearID), m.Recipient, m.Subject, m.Body,
		m.AttName, m.AttType, m.AttData, time.Now().Add(outboxBackoff(1)))
	return err
}

// PendingMailCount reports how many mails are parked (for /metrics).
func (s *Store) PendingMailCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM mail_outbox WHERE status='pending'`).Scan(&n)
	return n, err
}

// ProcessMailOutbox delivers every due parked mail through send and settles each
// row: sent on success, pending with a pushed-out next attempt on failure, and
// failed for good once outboxMaxAttempts is exhausted.
//
// The sender is injected so the retry bookkeeping is testable without an SMTP
// server, and so this package does not grow a config dependency. Outcomes land
// in the audit trail with the system actor: a delivery that finally worked —
// or finally did not — must be answerable later without grepping logs.
func (s *Store) ProcessMailOutbox(ctx context.Context,
	send func(ctx context.Context, to, subject, body, attName, attType string, attData []byte) error,
) (delivered, exhausted int, err error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, kind, COALESCE(neighbor_id,0), COALESCE(billing_year_id,0), recipient, subject, body,
		        att_name, att_type, COALESCE(att_data,''::bytea), attempts
		   FROM mail_outbox
		  WHERE status='pending' AND next_attempt_at <= now()
		  ORDER BY id
		  LIMIT 10`) // bounded per tick so a big backlog cannot stall the maintenance loop
	if err != nil {
		return 0, 0, err
	}
	var due []OutboxMail
	for rows.Next() {
		var m OutboxMail
		if err := rows.Scan(&m.ID, &m.Kind, &m.NeighborID, &m.BillingYearID, &m.Recipient,
			&m.Subject, &m.Body, &m.AttName, &m.AttType, &m.AttData, &m.Attempts); err != nil {
			_ = rows.Close()
			return 0, 0, err
		}
		due = append(due, m)
	}
	_ = rows.Close()
	if rows.Err() != nil {
		return 0, 0, rows.Err()
	}

	for _, m := range due {
		sendErr := send(ctx, m.Recipient, m.Subject, m.Body, m.AttName, m.AttType, m.AttData)
		if sendErr == nil {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE mail_outbox SET status='sent', sent_at=now(), attempts=attempts+1, last_error='' WHERE id=$1`,
				m.ID); err != nil {
				return delivered, exhausted, err
			}
			delivered++
			if m.Kind == "beleg" && m.NeighborID != 0 && m.BillingYearID != 0 {
				if err := s.RecordBelegSend(ctx, m.BillingYearID, m.NeighborID, "e-mail (Wiederholung)"); err != nil {
					slog.Warn("outbox: record beleg send failed", "id", m.ID, "err", err)
				}
			}
			if err := s.AddAudit(ctx, nil, "system", "mail_retry_sent", m.Kind, "", fmt.Sprintf(
				"%s nach %d Versuch(en) zugestellt an %s", m.Subject, m.Attempts+1, m.Recipient), ""); err != nil {
				slog.Warn("outbox: audit failed", "id", m.ID, "err", err)
			}
			continue
		}

		attempts := m.Attempts + 1
		if attempts >= outboxMaxAttempts {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE mail_outbox SET status='failed', attempts=$2, last_error=$3 WHERE id=$1`,
				m.ID, attempts, sendErr.Error()); err != nil {
				return delivered, exhausted, err
			}
			exhausted++
			if err := s.AddAudit(ctx, nil, "system", "mail_retry_failed", m.Kind, "", fmt.Sprintf(
				"%s endgültig NICHT zugestellt an %s (%d Versuche): %s",
				m.Subject, m.Recipient, attempts, sendErr.Error()), ""); err != nil {
				slog.Warn("outbox: audit failed", "id", m.ID, "err", err)
			}
			continue
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE mail_outbox SET attempts=$2, last_error=$3, next_attempt_at=$4 WHERE id=$1`,
			m.ID, attempts, sendErr.Error(), time.Now().Add(outboxBackoff(attempts+1))); err != nil {
			return delivered, exhausted, err
		}
	}
	return delivered, exhausted, nil
}

// PurgeSentMail removes delivered outbox rows older than the cutoff. Failed rows
// are kept: they are the record of what never arrived.
func (s *Store) PurgeSentMail(ctx context.Context, olderThan time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM mail_outbox WHERE status='sent' AND sent_at < $1`, olderThan)
	return err
}
