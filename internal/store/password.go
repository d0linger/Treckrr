package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/d0linger/treckrr/internal/auth"
)

// PasswordChange binds the password proof to the session being rotated.
type PasswordChange struct {
	UserID                                     int64
	CurrentPassword, NewPassword, CurrentToken string
	TTL, AbsoluteTTL                           time.Duration
	UserAgent, IP                              string
}

// ChangePassword rotates every session, including the caller's, atomically with
// the password and audit record. A stale password or revoked session cannot win
// a race against another credential change.
func (s *Store) ChangePassword(ctx context.Context, change PasswordChange) (string, error) {
	if len(change.CurrentPassword) > 72 || change.CurrentToken == "" {
		return "", ErrNotFound
	}
	hash, err := auth.HashPassword(change.NewPassword)
	if err != nil {
		return "", err
	}
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var currentHash string
	err = tx.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id=$1 AND NOT disabled FOR UPDATE`, change.UserID).Scan(&currentHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !auth.CheckPassword(currentHash, change.CurrentPassword) {
		return "", ErrNotFound
	}
	var validSession bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token=$2
		AND expires_at>now() AND created_at>now()-make_interval(secs=>$3))`,
		change.UserID, HashToken(change.CurrentToken), change.AbsoluteTTL.Seconds()).Scan(&validSession); err != nil {
		return "", err
	}
	if !validSession {
		return "", ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=$2, must_change_password=false WHERE id=$1`, change.UserID, hash); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=$1`, change.UserID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sessions (token,user_id,expires_at,user_agent,ip) VALUES ($1,$2,$3,$4,$5)`,
		HashToken(token), change.UserID, time.Now().Add(change.TTL), change.UserAgent, change.IP); err != nil {
		return "", err
	}
	if err := addAuditTx(ctx, tx, "password_change", "user", strconv.FormatInt(change.UserID, 10), "eigenes Passwort; alle bisherigen Sitzungen beendet"); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

// ResetPassword applies an administrator's password reset and session revocation
// together; a failure to record the audit also rolls back the credential change.
//
// The user's passkeys are revoked too. A reset is what an admin reaches for
// when an account is compromised, and a passkey the attacker enrolled would
// otherwise survive it and log straight back in — passkey login never asks for
// the password or TOTP. The user's own password change (ChangePassword) keeps
// them: that user proved the old password and is still signed in to re-check.
func (s *Store) ResetPassword(ctx context.Context, userID int64, password string, mustChange bool) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	result, err := tx.ExecContext(ctx, `UPDATE users SET password_hash=$2, must_change_password=$3 WHERE id=$1 AND NOT disabled`, userID, hash, mustChange)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return err
	}
	passkeys, err := revokePasskeysTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if err := addAuditTx(ctx, tx, "password_reset", "user", strconv.FormatInt(userID, 10),
		"durch Admin; Sitzungen beendet"+passkeyRevocationNote(passkeys)); err != nil {
		return err
	}
	return tx.Commit()
}

// revokePasskeysTx deletes every passkey of the user inside tx and returns how
// many there were.
func revokePasskeysTx(ctx context.Context, tx *sql.Tx, userID int64) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM webauthn_credentials WHERE user_id=$1`, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke passkeys: %w", err)
	}
	return res.RowsAffected()
}

// passkeyRevocationNote is the audit-detail suffix naming revoked passkeys.
func passkeyRevocationNote(n int64) string {
	if n == 0 {
		return ""
	}
	return "; " + strconv.FormatInt(n, 10) + " Passkey(s) widerrufen"
}
