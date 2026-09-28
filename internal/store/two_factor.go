package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// TwoFactorChange enables/disables a factor together with its recovery codes.
// AcceptedStep is the TOTP time-step the enrollment code matched; it is stored
// as totp_last_step so that very code cannot be replayed at login.
type TwoFactorChange struct {
	UserID         int64
	Enabled        bool
	Secret         string
	RecoveryHashes []string
	AcceptedStep   uint64
}

// PendingTwoFactorBinding returns a digest of the credential state a pending
// second login step depends on: the password hash, the TOTP factor and its
// last accepted step, and the number of unused recovery codes. The pending-2FA
// token is MACed over it, so a completed second step, a password change or an
// admin reset invalidates every token issued before it — without server-side
// token state. ErrNotFound for a missing or disabled account.
func (s *Store) PendingTwoFactorBinding(ctx context.Context, userID int64) ([]byte, error) {
	var (
		passwordHash, secret string
		enabled              bool
		lastStep             sql.NullInt64
		unused               int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT u.password_hash, u.totp_enabled, u.totp_secret, u.totp_last_step,
		        (SELECT count(*) FROM totp_recovery_codes c WHERE c.user_id=u.id AND c.used_at IS NULL)
		   FROM users u WHERE u.id=$1 AND NOT u.disabled`, userID).
		Scan(&passwordHash, &enabled, &secret, &lastStep, &unused)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%q|%t|%q|%t|%d|%d", passwordHash, enabled, secret, lastStep.Valid, lastStep.Int64, unused)
	return h.Sum(nil), nil
}

// ConfigureTwoFactor commits the factor, recovery codes and audit as one unit.
func (s *Store) ConfigureTwoFactor(ctx context.Context, change TwoFactorChange) error {
	if change.Enabled && (change.Secret == "" || len(change.RecoveryHashes) == 0) {
		return errors.New("2fa enrollment requires a secret and recovery codes")
	}
	secret := ""
	if change.Enabled {
		var err error
		secret, err = s.encryptTotp(change.Secret)
		if err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Enabling records the step the confirmation code matched, so the code typed
	// at enrollment is already consumed; disabling clears replay state with the
	// factor.
	var lastStep sql.NullInt64
	if change.Enabled && change.AcceptedStep > 0 {
		lastStep = sql.NullInt64{Int64: int64(change.AcceptedStep), Valid: true} //nosec G115 -- TOTP step (unix/30) fits int64 for millennia
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET totp_enabled=$2, totp_secret=$3, totp_last_step=$4 WHERE id=$1 AND NOT disabled`, change.UserID, change.Enabled, secret, lastStep)
	if err := activeUserUpdateResult(res, err); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM totp_recovery_codes WHERE user_id=$1`, change.UserID); err != nil {
		return err
	}
	if change.Enabled {
		for _, hash := range change.RecoveryHashes {
			if _, err := tx.ExecContext(ctx, `INSERT INTO totp_recovery_codes (user_id,code_hash) VALUES ($1,$2)`, change.UserID, hash); err != nil {
				return err
			}
		}
	}
	action := "2fa_disable"
	if change.Enabled {
		action = "2fa_enable"
	}
	if err := addAuditTx(ctx, tx, action, "user", strconv.FormatInt(change.UserID, 10), ""); err != nil {
		return err
	}
	return tx.Commit()
}
