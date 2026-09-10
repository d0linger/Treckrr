package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
)

// ReplaceRecoveryCodes atomically replaces a user's recovery codes with the
// given hashes (invalidating any previous, unused ones).
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Serialize concurrent regenerations for the same user: without this, two
	// transactions could each delete the visible rows and insert their own set
	// (neither sees the other's uncommitted inserts under READ COMMITTED),
	// leaving 2x codes instead of a clean replacement.
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM users WHERE id=$1 AND NOT disabled FOR UPDATE`, userID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM totp_recovery_codes WHERE user_id=$1`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO totp_recovery_codes (user_id, code_hash) VALUES ($1,$2)`, userID, h); err != nil {
			return err
		}
	}
	if err := addAuditTx(ctx, tx, "2fa_recovery_regenerate", "user", strconv.FormatInt(userID, 10), ""); err != nil {
		return err
	}
	return tx.Commit()
}

// ConsumeRecoveryCode marks a matching unused recovery code as used and reports
// whether one was found (single-use).
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID int64, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE totp_recovery_codes SET used_at = now()
		  WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, userID, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CountUnusedRecoveryCodes returns how many recovery codes remain unused.
func (s *Store) CountUnusedRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM totp_recovery_codes WHERE user_id=$1 AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}
