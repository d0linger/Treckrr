package store

import (
	"context"
	"errors"
	"strconv"
)

// TwoFactorChange enables/disables a factor together with its recovery codes.
type TwoFactorChange struct {
	UserID         int64
	Enabled        bool
	Secret         string
	RecoveryHashes []string
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
	res, err := tx.ExecContext(ctx, `UPDATE users SET totp_enabled=$2, totp_secret=$3, totp_last_step=NULL WHERE id=$1 AND NOT disabled`, change.UserID, change.Enabled, secret)
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
