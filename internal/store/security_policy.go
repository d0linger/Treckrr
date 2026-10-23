package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// ErrSecondFactorRequired prevents self-service removal of the last required
// factor. Explicit administrator recovery uses separate reset operations.
var ErrSecondFactorRequired = errors.New("security policy requires a second factor")

// Both factor-removal paths lock the same user before counting remaining
// factors. The subsequent statement gets a fresh READ COMMITTED snapshot after
// a concurrent removal commits; a count inside the locking statement would not.
func checkFactorRemovalTx(ctx context.Context, tx *sql.Tx, userID, credentialID int64, removingTOTP bool) error {
	var u models.User
	err := tx.QueryRowContext(ctx, `SELECT role, totp_enabled FROM users WHERE id=$1 AND NOT disabled FOR UPDATE`, userID).
		Scan(&u.Role, &u.TotpEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var p SecurityPolicy
	if err := tx.QueryRowContext(ctx, `SELECT require_mfa_admin, require_mfa_editor FROM security_policy WHERE id=1 FOR SHARE`).
		Scan(&p.RequireAdmin, &p.RequireEditor); err != nil {
		return err
	}
	if !p.Requires(u) || (!removingTOTP && u.TotpEnabled) {
		return nil
	}
	var remaining bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM webauthn_credentials WHERE user_id=$1 AND id<>$2)`, userID, credentialID).
		Scan(&remaining); err != nil {
		return err
	}
	if !remaining {
		return ErrSecondFactorRequired
	}
	return nil
}

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
