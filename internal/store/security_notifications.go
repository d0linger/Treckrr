package store

import (
	"context"
	"database/sql"
)

// NotifyUser stores one notification for a single user, outside the role-wide
// sources. A repeated dedupe key is a no-op, so retries cannot duplicate it.
func (s *Store) NotifyUser(ctx context.Context, userID int64, n NotificationSource) error {
	n.Title = truncateNotificationText(n.Title, 200)
	n.Detail = truncateNotificationText(n.Detail, 1000)
	if err := validateNotificationSource(n); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO notifications (user_id,dedupe_key,kind,tone,title,detail,href)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (user_id,dedupe_key) DO NOTHING`,
		userID, n.DedupeKey, n.Kind, n.Tone, n.Title, n.Detail, n.Href)
	return err
}

// RememberDevice records a sign-in from a coarse browser/OS label. isNew is
// true when the account has signed in before but never from this label; the
// very first device of an account is not "new".
func (s *Store) RememberDevice(ctx context.Context, userID int64, device string) (isNew bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	var known int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user_known_devices WHERE user_id=$1`, userID).Scan(&known); err != nil {
		return false, err
	}
	var inserted bool
	err = tx.QueryRowContext(ctx, `INSERT INTO user_known_devices (user_id, device) VALUES ($1, $2)
		ON CONFLICT (user_id, device) DO UPDATE SET last_seen = now()
		RETURNING (xmax = 0)`, userID, device).Scan(&inserted)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return inserted && known > 0, tx.Commit()
}
