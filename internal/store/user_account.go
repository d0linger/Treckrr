package store

import (
	"context"
	"fmt"
	"strconv"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/models"
)

// NewAccount contains the administrator-controlled account creation policy.
type NewAccount struct {
	Username, Password, Role string
	MustChangePassword       bool
}

// CreateAccount inserts credentials, forced-change policy and audit atomically.
func (s *Store) CreateAccount(ctx context.Context, account NewAccount) (int64, error) {
	if account.Role != models.RoleAdmin && account.Role != models.RoleEditor && account.Role != models.RoleViewer {
		return 0, fmt.Errorf("invalid role")
	}
	hash, err := auth.HashPassword(account.Password)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var id int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO users (username,password_hash,role,is_admin,must_change_password)
		VALUES ($1,$2,$3,$4,$5) RETURNING id`, account.Username, hash, account.Role, account.Role == models.RoleAdmin, account.MustChangePassword).Scan(&id); err != nil {
		return 0, err
	}
	if err := addAuditTx(ctx, tx, "create", "user", strconv.FormatInt(id, 10), account.Username+" ("+account.Role+")"); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}
