package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"treckrr/internal/models"
)

// EnsureAdmin creates the bootstrap admin from env config if it does not exist.
// If the user exists, its password is reset to the configured value so the
// admin can always regain access via Docker ENV.
func (s *Store) EnsureAdmin(ctx context.Context, username, password string) error {
	var (
		id      int64
		isAdmin bool
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, is_admin FROM users WHERE username=$1`, username).Scan(&id, &isAdmin)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.CreateUser(ctx, username, password, models.RoleAdmin); err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("look up admin: %w", err)
	}
	if err := s.UpdatePassword(ctx, id, password); err != nil {
		return err
	}
	if !isAdmin {
		return s.SetAdmin(ctx, id, true)
	}
	return nil
}
