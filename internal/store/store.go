// Package store contains all database access for the application.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"

	"treckrr/internal/auth"
	"treckrr/internal/models"
)

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// Store wraps the database connection pool.
type Store struct {
	db  *sql.DB
	key []byte
}

// New returns a Store backed by the given pool.
func New(db *sql.DB, encryptionKey string) *Store {
	h := sha256.Sum256([]byte(encryptionKey))
	return &Store{db: db, key: h[:]}
}

// ---- Users ---------------------------------------------------------------

// userCols is the shared column list for scanning a models.User.
const userCols = `id, username, role, is_admin, must_change_password, totp_enabled, created_at`

func scanUser(sc scanner) (models.User, error) {
	var u models.User
	if err := sc.Scan(&u.ID, &u.Username, &u.Role, &u.IsAdmin,
		&u.MustChangePassword, &u.TotpEnabled, &u.CreatedAt); err != nil {
		return u, err
	}
	// Role is authoritative; keep the legacy IsAdmin flag in sync.
	u.IsAdmin = u.Role == models.RoleAdmin
	return u, nil
}

// CreateUser inserts a new user with a hashed password and role.
func (s *Store) CreateUser(ctx context.Context, username, password, role string) (int64, error) {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRowContext(ctx,
		`INSERT INTO users (username, password_hash, role, is_admin)
		 VALUES ($1,$2,$3,$4) RETURNING id`,
		username, hash, role, role == models.RoleAdmin).Scan(&id)
	return id, err
}

// SetRole updates a user's role (and keeps is_admin in sync).
func (s *Store) SetRole(ctx context.Context, userID int64, role string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET role=$1, is_admin=$2 WHERE id=$3`, role, role == models.RoleAdmin, userID)
	return err
}

// SetMustChangePassword flags/unflags a forced password change on next login.
func (s *Store) SetMustChangePassword(ctx context.Context, userID int64, must bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET must_change_password=$1 WHERE id=$2`, must, userID)
	return err
}

// UpdatePassword sets a new password for the given user.
func (s *Store) UpdatePassword(ctx context.Context, userID int64, password string) error {
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET password_hash=$1 WHERE id=$2`, hash, userID)
	return err
}

// SetAdmin toggles admin by mapping to the role model (kept for compatibility).
func (s *Store) SetAdmin(ctx context.Context, userID int64, isAdmin bool) error {
	role := models.RoleEditor
	if isAdmin {
		role = models.RoleAdmin
	}
	return s.SetRole(ctx, userID, role)
}

// DeleteUser removes a user and their sessions.
func (s *Store) DeleteUser(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID)
	return err
}

// CountAdmins returns the number of admin users.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM users WHERE is_admin`).Scan(&n)
	return n, err
}

// AuthenticateUser validates credentials and returns the user on success.
func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (*models.User, error) {
	var (
		u    models.User
		hash string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT `+userCols+`, password_hash FROM users WHERE username=$1`, username).
		Scan(&u.ID, &u.Username, &u.Role, &u.IsAdmin, &u.MustChangePassword,
			&u.TotpEnabled, &u.CreatedAt, &hash)

	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Mitigation: if the user was not found, perform a dummy bcrypt comparison
	// anyway. This makes the response time similar for both existing and
	// non-existent usernames, preventing username enumeration via timing.
	if errors.Is(err, sql.ErrNoRows) {
		// A hardcoded dummy hash (cost 10).
		const dummy = "$2a$10$KOzr2pVHoGzHnk12ftvS/u0vPyHMDxZpy/.KV/3DZK2AqKfftv1mi"
		_ = auth.CheckPassword(dummy, password)
		return nil, ErrNotFound
	}

	if !auth.CheckPassword(hash, password) {
		return nil, ErrNotFound
	}
	u.IsAdmin = u.Role == models.RoleAdmin
	return &u, nil
}

const totpPrefix = "v1:"

// GetTotpSecret returns the stored TOTP secret for a user (may be empty).
func (s *Store) GetTotpSecret(ctx context.Context, userID int64) (string, error) {
	var secret string
	err := s.db.QueryRowContext(ctx,
		`SELECT totp_secret FROM users WHERE id=$1`, userID).Scan(&secret)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(secret, totpPrefix) {
		return auth.Decrypt(strings.TrimPrefix(secret, totpPrefix), s.key)
	}
	return secret, nil
}

// SetTotp enables/disables TOTP and stores the secret.
func (s *Store) SetTotp(ctx context.Context, userID int64, enabled bool, secret string) error {
	if secret != "" {
		enc, err := auth.Encrypt(secret, s.key)
		if err != nil {
			return err
		}
		secret = totpPrefix + enc
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_enabled=$1, totp_secret=$2 WHERE id=$3`, enabled, secret, userID)
	return err
}

// GetUser returns a user by id.
func (s *Store) GetUser(ctx context.Context, id int64) (*models.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ListUsers returns all users ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]models.User, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- Sessions ------------------------------------------------------------

// CreateSession stores a new session token for a user with client metadata.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, userAgent, ip string) (string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at, user_agent, ip)
		 VALUES ($1,$2,$3,$4,$5)`,
		token, userID, time.Now().Add(ttl), userAgent, ip)
	return token, err
}

// UserFromSession resolves a session token to its (non-expired) user. On each
// hit it refreshes last-seen and slides the expiry forward by slideTTL, so an
// actively-used session stays alive (rolling window) and only expires after
// slideTTL of inactivity.
func (s *Store) UserFromSession(ctx context.Context, token string, slideTTL time.Duration) (*models.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.role, u.is_admin, u.must_change_password, u.totp_enabled, u.created_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token=$1 AND s.expires_at > now()`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen=now(), expires_at=now() + make_interval(secs => $2) WHERE token=$1`,
		token, slideTTL.Seconds())
	return &u, nil
}

// ListSessionsForUser returns a user's active sessions, newest activity first.
func (s *Store) ListSessionsForUser(ctx context.Context, userID int64) ([]models.Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT token, user_id, user_agent, ip, last_seen, created_at, expires_at
		   FROM sessions WHERE user_id=$1 AND expires_at > now()
		  ORDER BY last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Session
	for rows.Next() {
		var se models.Session
		if err := rows.Scan(&se.Token, &se.UserID, &se.UserAgent, &se.IP,
			&se.LastSeen, &se.Created, &se.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// DeleteUserSessionsExcept ends all of a user's sessions except one token.
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID int64, keep string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id=$1 AND token <> $2`, userID, keep)
	return err
}

// DeleteSessionForUser removes one session belonging to the given user.
func (s *Store) DeleteSessionForUser(ctx context.Context, userID int64, token string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id=$1 AND token=$2`, userID, token)
	return err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token=$1`, token)
	return err
}

// PurgeExpiredSessions deletes sessions past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return err
}
