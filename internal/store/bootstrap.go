package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/d0linger/treckrr/internal/models"
)

// EnsureAdmin creates the bootstrap admin from env config if it does not exist,
// forcing a password change at first login so the env value is rotated off.
//
// An EXISTING admin's password is left untouched on a normal boot: otherwise a
// password set through the UI would be silently reverted to the env value on
// every restart, and that env value would become a permanent standing credential
// that in-app rotation could never revoke. Pass reset=true (ADMIN_PASSWORD_RESET)
// as a deliberate break-glass to reset the password and revoke live sessions.
func (s *Store) EnsureAdmin(ctx context.Context, username, password string, reset bool) error {
	var (
		id       int64
		isAdmin  bool
		disabled bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, role='admin', disabled FROM users WHERE username=$1`, username).Scan(&id, &isAdmin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		_, err := s.CreateAccount(ctx, NewAccount{Username: username, Password: password, Role: models.RoleAdmin, MustChangePassword: true})
		if err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up admin: %w", err)
	}
	if disabled {
		// Retired identities must not be resurrected by stale environment config.
		if reset {
			return fmt.Errorf("bootstrap admin is disabled; choose an active administrator")
		}
		return nil
	}
	if !isAdmin {
		if err := s.SetAdmin(ctx, id, true); err != nil {
			return err
		}
	}
	if !reset {
		return nil
	}
	// Break-glass: reset to the env password, force a change, and revoke sessions.
	return s.ResetPassword(ctx, id, password, true)
}
