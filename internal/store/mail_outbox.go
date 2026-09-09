package store

import (
	"context"
	"database/sql"
	"errors"
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
// nullable turns id 0 ("no linked record") into NULL for foreign-key columns —
// id 0 never exists, and inserting it would violate the constraint.
func nullable(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func (s *Store) EnqueueMail(ctx context.Context, m OutboxMail) error {
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
		// Claim the row before dialing SMTP. Selecting and then sending leaves a
		// window in which a second app instance — or a tick that overlaps a slow
		// SMTP dialog — reads the same still-pending row and delivers the same
		// invoice twice. This one statement both counts the attempt and pushes
		// next_attempt_at past now(), so the row stops being due the moment it is
		// claimed; whoever loses the race updates nothing and skips it.
		attempts := m.Attempts + 1
		var claimed int64
		claimErr := s.db.QueryRowContext(ctx,
			`UPDATE mail_outbox SET attempts=$2, next_attempt_at=$3
			  WHERE id=$1 AND status='pending' AND next_attempt_at <= now()
			  RETURNING id`,
			m.ID, attempts, time.Now().Add(outboxBackoff(attempts+1))).Scan(&claimed)
		if errors.Is(claimErr, sql.ErrNoRows) {
			continue // another worker got there first
		}
		if claimErr != nil {
			return delivered, exhausted, claimErr
		}

		sendErr := send(ctx, m.Recipient, m.Subject, m.Body, m.AttName, m.AttType, m.AttData)
		if sendErr == nil {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE mail_outbox SET status='sent', sent_at=now(), last_error='' WHERE id=$1`,
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

		// attempts and next_attempt_at were already written by the claim above;
		// only the outcome of THIS attempt still has to be recorded.
		if attempts >= outboxMaxAttempts {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE mail_outbox SET status='failed', last_error=$2 WHERE id=$1`,
				m.ID, sendErr.Error()); err != nil {
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
			`UPDATE mail_outbox SET last_error=$2 WHERE id=$1`, m.ID, sendErr.Error()); err != nil {
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
