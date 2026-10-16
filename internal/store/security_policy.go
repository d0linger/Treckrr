package store

import (
	"context"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// SecurityPolicy says which roles must sign in with a second factor (TOTP or
// passkey) and how long a newly affected account may still work without one.
type SecurityPolicy struct {
	RequireAdmin  bool
	RequireEditor bool
	GraceDays     int
	EnforcedSince *time.Time
	UpdatedAt     time.Time
}

// Requires reports whether the policy requires a second factor for the user's
// role. Viewers are never required: they cannot change data.
func (p SecurityPolicy) Requires(u models.User) bool {
	switch {
	case u.Disabled:
		return false
	case u.Role == models.RoleAdmin:
		return p.RequireAdmin
	case u.Role == models.RoleEditor:
		return p.RequireEditor
	}
	return false
}

// Deadline returns when an account without a second factor stops being able
// to use the app: the grace period counted from the later of the moment the
// requirement started and the account's creation. ok=false means the policy
// does not apply to this account (role not required, or a factor exists).
func (p SecurityPolicy) Deadline(u models.User) (time.Time, bool) {
	if !p.Requires(u) || u.HasSecondFactor() {
		return time.Time{}, false
	}
	start := u.CreatedAt
	if p.EnforcedSince != nil && p.EnforcedSince.After(start) {
		start = *p.EnforcedSince
	}
	return start.Add(time.Duration(p.GraceDays) * 24 * time.Hour), true
}

// GetSecurityPolicy reads the single policy row.
func (s *Store) GetSecurityPolicy(ctx context.Context) (SecurityPolicy, error) {
	var p SecurityPolicy
	err := s.db.QueryRowContext(ctx, `SELECT require_mfa_admin, require_mfa_editor, grace_days, enforced_since, updated_at
		FROM security_policy WHERE id = 1`).Scan(&p.RequireAdmin, &p.RequireEditor, &p.GraceDays, &p.EnforcedSince, &p.UpdatedAt)
	return p, err
}

// UpdateSecurityPolicy stores the policy. A role that becomes newly required
// restarts the grace period for everyone; removing roles keeps it, and an
// empty requirement clears it.
func (s *Store) UpdateSecurityPolicy(ctx context.Context, requireAdmin, requireEditor bool, graceDays int) (SecurityPolicy, error) {
	var p SecurityPolicy
	err := s.db.QueryRowContext(ctx, `
		UPDATE security_policy SET
		       enforced_since = CASE
		           WHEN NOT ($1 OR $2) THEN NULL
		           WHEN ($1 AND NOT require_mfa_admin) OR ($2 AND NOT require_mfa_editor) THEN now()
		           ELSE enforced_since END,
		       require_mfa_admin = $1, require_mfa_editor = $2, grace_days = $3, updated_at = now()
		 WHERE id = 1
		RETURNING require_mfa_admin, require_mfa_editor, grace_days, enforced_since, updated_at`,
		requireAdmin, requireEditor, graceDays).Scan(&p.RequireAdmin, &p.RequireEditor, &p.GraceDays, &p.EnforcedSince, &p.UpdatedAt)
	return p, err
}
