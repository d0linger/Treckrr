// Package store contains all database access for the application.
package store

import (
	"context"
	"crypto/hkdf"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/models"
)

// sessionTouchInterval is how stale a session row may get before a request
// refreshes it. See UserFromSession for why this is throttled rather than done on
// every request; the profile page shows last activity at a coarser resolution
// than this anyway.
const sessionTouchInterval = 5 * time.Minute

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// ErrGutschriftTooLarge is returned when a credit note would exceed the invoice's
// remaining (uncredited) gross amount.
var ErrGutschriftTooLarge = errors.New("gutschrift exceeds invoice")

// ErrInvoiceIncomplete is returned when issuance is attempted on content that does
// not satisfy the § 11 UStG mandatory fields (a store-side backstop to the UI).
var ErrInvoiceIncomplete = errors.New("invoice content incomplete (§11)")

// ErrLastAdmin is returned when a role/delete change would leave no admin user.
var ErrLastAdmin = errors.New("cannot remove the last admin")

// Store wraps the database connection pool.
type Store struct {
	db      *sql.DB
	key     []byte // legacy TOTP-at-rest key = sha256(encryptionKey), reads v1: secrets
	totpKey []byte // TOTP-at-rest key = HKDF(encryptionKey), key-separated, writes v2:
}

// New returns a Store backed by the given pool.
func New(db *sql.DB, encryptionKey string) *Store {
	h := sha256.Sum256([]byte(encryptionKey))
	return &Store{db: db, key: h[:], totpKey: deriveTotpKey(encryptionKey)}
}

// DBStats exposes the connection-pool statistics for the /metrics endpoint.
func (s *Store) DBStats() sql.DBStats { return s.db.Stats() }

// deriveTotpKey derives a purpose-separated 32-byte key for encrypting TOTP
// secrets at rest via HKDF-SHA256, so the at-rest cipher key is distinct from
// the raw secret used elsewhere for HMAC (CSRF / pending-2FA / session tokens)
// rather than a bare sha256 of the same secret.
func deriveTotpKey(encryptionKey string) []byte {
	key, err := hkdf.Key(sha256.New, []byte(encryptionKey), nil, "treckrr-totp-at-rest-v2", 32)
	if err != nil { // never happens for a 32-byte output; fall back defensively
		h := sha256.Sum256([]byte(encryptionKey + ":totp"))
		return h[:]
	}
	return key
}

// Ping verifies the database is reachable — a cheap, side-effect-free probe for
// the health endpoint (maintenance purges run on a timer, not per health check).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// ---- Users ---------------------------------------------------------------

// userCols is the shared column list for scanning a models.User.
const userCols = `id, username, email, role, is_admin, must_change_password, totp_enabled, created_at`

func scanUser(sc scanner) (models.User, error) {
	var u models.User
	if err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.IsAdmin,
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

// UpdateUserAccount updates a user's username and e-mail. Returns an error if
// the username is already taken (the users.username UNIQUE constraint).
func (s *Store) UpdateUserAccount(ctx context.Context, userID int64, username, email string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET username=$1, email=$2 WHERE id=$3`, username, email, userID)
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

// SetAdmin toggles admin by mapping to the role model. It routes through the
// guarded SetRoleSafe so it can never demote the last admin (bootstrap only
// promotes, but the guard keeps any future caller safe).
func (s *Store) SetAdmin(ctx context.Context, userID int64, isAdmin bool) error {
	role := models.RoleEditor
	if isAdmin {
		role = models.RoleAdmin
	}
	return s.SetRoleSafe(ctx, userID, role)
}

// adminLockKey serializes admin-count decisions (SetRoleSafe/DeleteUserSafe) via a
// transaction-level advisory lock, so two concurrent changes can't race the last
// admin down to zero.
const adminLockKey = 4712

// SetRoleSafe changes a user's role but refuses to demote the last admin. The
// admin check and the update run in one transaction under an advisory lock and it
// fails closed on any error (SH-04). Returns ErrLastAdmin if the change would
// leave no admin, ErrNotFound if the user is gone.
func (s *Store) SetRoleSafe(ctx context.Context, userID int64, role string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, adminLockKey); err != nil {
		return err
	}
	var wasAdmin bool
	if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&wasAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if wasAdmin && role != models.RoleAdmin {
		var admins int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_admin`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET role=$1, is_admin=$2 WHERE id=$3`, role, role == models.RoleAdmin, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUserSafe removes a user but refuses to delete the last admin — the admin
// check and the delete are atomic under the same advisory lock, failing closed
// (SH-04). Returns ErrLastAdmin / ErrNotFound as SetRoleSafe does.
func (s *Store) DeleteUserSafe(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, adminLockKey); err != nil {
		return err
	}
	var isAdmin bool
	if err := tx.QueryRowContext(ctx, `SELECT is_admin FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&isAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if isAdmin {
		var admins int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE is_admin`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id=$1`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// AuthenticateUser validates credentials and returns the user on success.
func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (*models.User, error) {
	// Reject oversized credentials before any DB query or bcrypt work: an
	// over-limit value can never match an account, so this keeps a huge
	// username/password from driving a query or a (dummy) bcrypt comparison.
	// The byte-length password check is O(1); count username runes incrementally
	// and bail at the 101st rune so a giant username is never fully scanned.
	if len(password) > 72 {
		return nil, ErrNotFound
	}
	runes := 0
	for range username {
		runes++
		if runes > 100 {
			return nil, ErrNotFound
		}
	}
	var (
		u    models.User
		hash string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT `+userCols+`, password_hash FROM users WHERE username=$1`, username).
		Scan(&u.ID, &u.Username, &u.Email, &u.Role, &u.IsAdmin, &u.MustChangePassword,
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

const (
	totpPrefix   = "v1:" // legacy at-rest key = sha256(encryptionKey)
	totpPrefixV2 = "v2:" // current at-rest key = HKDF(encryptionKey)
)

// decryptTotp reverses the stored at-rest encryption, choosing the key from the
// version prefix (v2: HKDF, v1: legacy sha256). An unprefixed value is a very old
// plaintext secret and is returned as-is.
func (s *Store) decryptTotp(stored string) (string, error) {
	switch {
	case strings.HasPrefix(stored, totpPrefixV2):
		return auth.Decrypt(strings.TrimPrefix(stored, totpPrefixV2), s.totpKey)
	case strings.HasPrefix(stored, totpPrefix):
		return auth.Decrypt(strings.TrimPrefix(stored, totpPrefix), s.key)
	default:
		return stored, nil
	}
}

// encryptTotp encrypts a secret for storage in the current (v2) format.
func (s *Store) encryptTotp(secret string) (string, error) {
	enc, err := auth.Encrypt(secret, s.totpKey)
	if err != nil {
		return "", err
	}
	return totpPrefixV2 + enc, nil
}

// MigrateTotpSecretsToV2 re-encrypts every stored TOTP secret that is not already
// in v2 format (legacy v1 or very old unprefixed plaintext) into v2, so plaintext
// seeds no longer linger in the database or its backups (T-06). Idempotent — a row
// is only rewritten if it still holds the value we read — and safe to run on boot.
func (s *Store) MigrateTotpSecretsToV2(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, totp_secret FROM users WHERE totp_secret <> '' AND totp_secret NOT LIKE 'v2:%'`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type row struct {
		id     int64
		stored string
	}
	var todo []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.stored); err != nil {
			return 0, err
		}
		todo = append(todo, r)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, r := range todo {
		plain, err := s.decryptTotp(r.stored)
		if err != nil {
			// A single undecryptable seed (corrupt/wrong key) must not block the rest —
			// log it and skip; encryption/DB errors below stay fatal.
			slog.Warn("totp migrate: skipping user (decrypt failed)", "user", r.id, "err", err)
			continue
		}
		enc, err := s.encryptTotp(plain)
		if err != nil {
			return n, fmt.Errorf("totp migrate user %d: %w", r.id, err)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE users SET totp_secret=$2 WHERE id=$1 AND totp_secret=$3`, r.id, enc, r.stored); err != nil {
			return n, fmt.Errorf("totp migrate user %d: %w", r.id, err)
		}
		n++
	}
	return n, nil
}

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
	return s.decryptTotp(secret)
}

// SetTotp enables/disables TOTP and stores the secret (encrypted, current format).
func (s *Store) SetTotp(ctx context.Context, userID int64, enabled bool, secret string) error {
	if secret != "" {
		enc, err := s.encryptTotp(secret)
		if err != nil {
			return err
		}
		secret = enc
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_enabled=$1, totp_secret=$2 WHERE id=$3`, enabled, secret, userID)
	return err
}

// AcceptTotpStep records a just-matched TOTP time-step for replay protection.
// It is an atomic compare-and-set: the step is stored (and true returned) only
// when it is strictly newer than the last accepted one, so a code replayed
// within its validity window — even concurrently — is rejected exactly once.
func (s *Store) AcceptTotpStep(ctx context.Context, userID int64, step uint64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_last_step = $2
		  WHERE id = $1 AND (totp_last_step IS NULL OR totp_last_step < $2)`,
		userID, int64(step)) //nosec G115 -- TOTP step (unix/30) fits int64 for millennia
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
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

// HashToken maps a raw session token to the value stored in the database. The
// raw token lives only in the user's cookie; the DB keeps its SHA-256 so a read
// of the sessions table (backup leak, replica, SQL injection) cannot yield a
// usable cookie. A raw 256-bit token has full entropy, so an unsalted hash is
// sufficient (same rationale as recovery-code hashing).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateSession stores a new session token for a user with client metadata. The
// raw token is returned to the caller (for the cookie); only its hash is stored.
func (s *Store) CreateSession(ctx context.Context, userID int64, ttl time.Duration, userAgent, ip string) (string, error) {
	token, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO sessions (token, user_id, expires_at, user_agent, ip)
		 VALUES ($1,$2,$3,$4,$5)`,
		HashToken(token), userID, time.Now().Add(ttl), userAgent, ip)
	return token, err
}

// UserFromSession resolves a session token to its (non-expired) user. On each
// hit it refreshes last-seen and slides the expiry forward by slideTTL, so an
// actively-used session stays alive (rolling window) and only expires after
// slideTTL of inactivity. absoluteTTL additionally caps the total lifetime from
// creation, so a stolen token can't be kept alive indefinitely by sliding.
func (s *Store) UserFromSession(ctx context.Context, token string, slideTTL, absoluteTTL time.Duration) (*models.User, error) {
	th := HashToken(token)
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT u.id, u.username, u.email, u.role, u.is_admin, u.must_change_password, u.totp_enabled, u.created_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token=$1 AND s.expires_at > now()
		    AND s.created_at > now() - make_interval(secs => $2)`, th, absoluteTTL.Seconds()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	// Throttled slide: only touch the row when it has not been touched recently.
	// Refreshing on EVERY authenticated request meant one UPDATE per page view on
	// the same hot row — WAL volume, a dead tuple each time for vacuum to collect,
	// and cross-tab contention on a single row for a value that changes nothing
	// anyone can observe at that resolution.
	//
	// It costs nothing in behavior: an actively used session is still refreshed
	// at least every sessionTouchInterval, which is four orders of magnitude
	// shorter than the 30-day sliding window it maintains. last_seen is NOT NULL
	// with a default, so the predicate can never be NULL and skip forever.
	_, _ = s.db.ExecContext(ctx,
		`UPDATE sessions
		    SET last_seen=now(), expires_at=now() + make_interval(secs => $2)
		  WHERE token=$1 AND last_seen < now() - make_interval(secs => $3)`,
		th, slideTTL.Seconds(), sessionTouchInterval.Seconds())
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

// DeleteUserSessionsExcept ends all of a user's sessions except the one holding
// the given raw token. An empty keep deletes every session (used by admin
// reset / role change). keep is a raw cookie token, so it is hashed to match
// the stored value.
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID int64, keep string) error {
	keepHash := keep
	if keep != "" {
		keepHash = HashToken(keep)
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id=$1 AND token <> $2`, userID, keepHash)
	return err
}

// DeleteSessionForUser removes one session belonging to the given user. The
// identifier is the stored token hash (as surfaced by ListSessionsForUser and
// posted back from the profile page), so it is matched as-is — never a raw
// token. Scoped by userID, so a user can only revoke their own sessions.
//
// Reports whether a row was actually deleted: a stale profile tab can post the
// hash of a session that already expired or was revoked elsewhere, and a DELETE
// matching nothing is not an error. Callers must not report success (or write an
// audit record) on a no-op.
func (s *Store) DeleteSessionForUser(ctx context.Context, userID int64, tokenHash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id=$1 AND token=$2`, userID, tokenHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DeleteSession removes a session (logout). token is the raw cookie value.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token=$1`, HashToken(token))
	return err
}

// PurgeExpiredSessions deletes sessions past their expiry.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	return err
}

// ResetTotpForUser disables TOTP, discards the recovery codes and revokes every
// session of the user — in ONE transaction.
//
// The three writes have to succeed or fail together. Done separately, a failure
// after the first left the account in a state strictly worse than before the
// admin pressed the button: the second factor switched off while the sessions it
// was protecting stay alive. Since that button exists mainly for a compromised
// account or a lost authenticator, a half-applied reset is the one outcome it
// must never produce.
func (s *Store) ResetTotpForUser(ctx context.Context, userID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET totp_enabled=false, totp_secret='' WHERE id=$1`, userID); err != nil {
		return fmt.Errorf("reset totp: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM totp_recovery_codes WHERE user_id=$1`, userID); err != nil {
		return fmt.Errorf("reset totp: clear recovery codes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
		return fmt.Errorf("reset totp: revoke sessions: %w", err)
	}
	return tx.Commit()
}
