package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/models"
)

// EnsureAdmin creates the bootstrap admin from env config when the instance has
// no active administrator yet, forcing a password change at first login so the
// env value is rotated off.
//
// A normal boot never changes an existing account. An EXISTING admin's password
// is left untouched: otherwise a password set through the UI would be silently
// reverted to the env value on every restart, and that env value would become a
// permanent standing credential that in-app rotation could never revoke. An
// existing NON-admin named ADMIN_USERNAME is not promoted either: doing so
// undid every demotion of that account on the next restart, and handed admin
// rights to whoever later got that name. Nor is a missing ADMIN_USERNAME
// re-created while another admin exists (e.g. after the bootstrap account was
// renamed), which would revive the env password as a live admin credential.
//
// Pass reset=true (ADMIN_PASSWORD_RESET) as a deliberate break-glass: it
// promotes the account if needed, resets its password, and revokes its live
// sessions and passkeys (or creates it when missing).
func (s *Store) EnsureAdmin(ctx context.Context, username, password string, reset bool) error {
	var (
		id       int64
		isAdmin  bool
		disabled bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, role='admin', disabled FROM users WHERE username=$1`, username).Scan(&id, &isAdmin, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		if !reset {
			hasAdmin, err := s.activeAdminExists(ctx)
			if err != nil {
				return err
			}
			if hasAdmin {
				slog.Info("bootstrap admin not created: an active administrator already exists",
					"admin_username", username)
				return nil
			}
		}
		if err := auth.ValidatePassword(password); err != nil {
			return fmt.Errorf("validate bootstrap admin password: %w", err)
		}
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
	if !reset {
		if !isAdmin {
			slog.Warn("ADMIN_USERNAME names an existing non-admin account; it is NOT promoted. "+
				"Set ADMIN_PASSWORD_RESET=true once to promote it and reset its password deliberately.",
				"admin_username", username)
			if hasAdmin, err := s.activeAdminExists(ctx); err == nil && !hasAdmin {
				slog.Error("no active administrator exists; user management stays unavailable until ADMIN_PASSWORD_RESET is used",
					"admin_username", username)
			}
		}
		return nil
	}
	// Validate before any mutation: a rejected break-glass credential must not
	// promote an existing non-admin account as a side effect.
	if err := auth.ValidatePassword(password); err != nil {
		return fmt.Errorf("validate bootstrap admin reset password: %w", err)
	}
	if !isAdmin {
		slog.Warn("ADMIN_PASSWORD_RESET: promoting the ADMIN_USERNAME account to administrator",
			"admin_username", username)
		if err := s.SetAdmin(ctx, id, true); err != nil {
			return err
		}
	}
	// Break-glass: reset to the env password, force a change, and revoke
	// sessions and passkeys.
	return s.ResetPassword(ctx, id, password, true)
}

// activeAdminExists reports whether any enabled administrator account exists.
func (s *Store) activeAdminExists(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM users WHERE role='admin' AND NOT disabled)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("look up admins: %w", err)
	}
	return exists, nil
}
