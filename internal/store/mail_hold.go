package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// HeldMail is the admin-panel view of one outbox intent parked after a restore.
// It deliberately omits the body and attachment bytes.
type HeldMail struct {
	ID        int64
	Kind      string
	Recipient string
	Subject   string
	Attempts  int
	CreatedAt time.Time
	HeldAt    time.Time
	LastError string
}

// holdOutboxAfterRestoreTx parks every non-terminal intent that came back with
// a restored backup. The backup cannot know whether such a mail was delivered
// after it was taken: sending it again could mean a second invoice or Mahnung
// for a neighbor, so an operator must release or discard each one.
func holdOutboxAfterRestoreTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE mail_outbox
		   SET last_error='Nach Wiederherstellung angehalten (Status vorher: ' || status || ')'
		                  || CASE WHEN last_error='' THEN '' ELSE ' · ' || last_error END,
		       status='held', held_at=now(), claimed_at=NULL, delivery_phase=NULL
		 WHERE status IN ('pending','sending')`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n > 0 {
		ctx = withSystemAuditActor(ctx)
		if err := addAuditTx(ctx, tx, "mail_held_after_restore", "mail_outbox", "",
			fmt.Sprintf("%d E-Mail(s) nach Wiederherstellung angehalten; Freigabe durch Admin erforderlich", n)); err != nil {
			return 0, err
		}
	}
	return n, nil
}

// withSystemAuditActor keeps an authenticated actor and falls back to "system".
func withSystemAuditActor(ctx context.Context) context.Context {
	if actor, _ := ctx.Value(auditActorKey{}).(AuditActor); actor.Username != "" {
		return ctx
	}
	return WithAuditActor(ctx, AuditActor{Username: "system"})
}

// HeldMailCount reports how many intents wait for operator release.
func (s *Store) HeldMailCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM mail_outbox WHERE status='held'`).Scan(&n)
	return n, err
}

// ListHeldMail returns the parked intents, oldest first, without payloads.
func (s *Store) ListHeldMail(ctx context.Context, limit int) ([]HeldMail, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, kind, recipient, subject, attempts, created_at,
		       COALESCE(held_at, created_at), last_error
		  FROM mail_outbox
		 WHERE status='held'
		 ORDER BY id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HeldMail
	for rows.Next() {
		var h HeldMail
		if err := rows.Scan(&h.ID, &h.Kind, &h.Recipient, &h.Subject, &h.Attempts,
			&h.CreatedAt, &h.HeldAt, &h.LastError); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ReleaseHeldMail returns one held intent to the delivery queue; the next
// outbox run sends it. ErrNotFound means the row is missing or not held.
func (s *Store) ReleaseHeldMail(ctx context.Context, id int64) (HeldMail, error) {
	return s.settleHeldMail(ctx, id, `
		UPDATE mail_outbox
		   SET status='pending', held_at=NULL, next_attempt_at=now(), last_error=''
		 WHERE id=$1 AND status='held'
		 RETURNING id, kind, recipient, subject, attempts, created_at, now(), last_error`,
		"mail_held_released", "zur Zustellung freigegeben")
}

// DiscardHeldMail ends one held intent without sending it. The row becomes a
// terminal failure so its metadata stays visible until the retention purge.
func (s *Store) DiscardHeldMail(ctx context.Context, id int64) (HeldMail, error) {
	return s.settleHeldMail(ctx, id, `
		UPDATE mail_outbox
		   SET status='failed', held_at=NULL, terminal_at=now(),
		       last_error='Nach Wiederherstellung verworfen (nicht gesendet)'
		 WHERE id=$1 AND status='held'
		 RETURNING id, kind, recipient, subject, attempts, created_at, now(), last_error`,
		"mail_held_discarded", "verworfen, nicht gesendet")
}

// settleHeldMail applies one operator decision and its audit line atomically.
func (s *Store) settleHeldMail(ctx context.Context, id int64, query, action, outcome string) (HeldMail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HeldMail{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var h HeldMail
	err = tx.QueryRowContext(ctx, query, id).Scan(&h.ID, &h.Kind, &h.Recipient, &h.Subject,
		&h.Attempts, &h.CreatedAt, &h.HeldAt, &h.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return HeldMail{}, ErrNotFound
	}
	if err != nil {
		return HeldMail{}, err
	}
	if err := addAuditTx(withSystemAuditActor(ctx), tx, action, h.Kind, fmt.Sprintf("%d", h.ID),
		fmt.Sprintf("%s an %s %s", h.Subject, h.Recipient, outcome)); err != nil {
		return HeldMail{}, err
	}
	if err := tx.Commit(); err != nil {
		return HeldMail{}, err
	}
	return h, nil
}
