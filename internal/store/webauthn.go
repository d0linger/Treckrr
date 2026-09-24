package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/d0linger/treckrr/internal/models"
)

// ErrInvalidWebauthnSignCount identifies persisted counter corruption. Values
// outside uint32 are rejected rather than wrapped or repaired.
var ErrInvalidWebauthnSignCount = errors.New("invalid WebAuthn signature counter")

// checkedWebauthnSignCount rejects persisted values that cannot be represented
// by the WebAuthn protocol counter type.
func checkedWebauthnSignCount(count int64) (uint32, error) {
	if count < 0 || count > int64(1<<32-1) {
		return 0, fmt.Errorf("%w: %d", ErrInvalidWebauthnSignCount, count)
	}
	return uint32(count), nil
}

// WebauthnHandle returns the user's stable random WebAuthn handle, generating
// and persisting one on first use. The handle (not the DB id) is what
// authenticators store, so it must never change for a user.
func (s *Store) WebauthnHandle(ctx context.Context, userID int64) ([]byte, error) {
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, err
	}
	// Atomic first-use assignment: COALESCE keeps any existing handle and only
	// writes the fresh one when the column is still NULL, so concurrent initial
	// calls (e.g. two register tabs) can never diverge to different handles.
	var handle []byte
	err := s.db.QueryRowContext(ctx,
		`UPDATE users SET webauthn_handle = COALESCE(webauthn_handle, $1)
		  WHERE id=$2 AND NOT disabled RETURNING webauthn_handle`, fresh, userID).Scan(&handle)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return handle, nil
}

// UserByWebauthnHandle resolves a WebAuthn handle to its user (for usernameless
// / discoverable login).
func (s *Store) UserByWebauthnHandle(ctx context.Context, handle []byte) (*models.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE webauthn_handle=$1 AND NOT disabled`, handle))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// ListWebauthnCredentials returns a user's registered passkeys.
func (s *Store) ListWebauthnCredentials(ctx context.Context, userID int64) ([]models.WebauthnCredential, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, credential_id, public_key, aaguid, sign_count, transports, name,
		        backup_eligible, backup_state, created_at, last_used_at
		   FROM webauthn_credentials WHERE user_id=$1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.WebauthnCredential
	for rows.Next() {
		var c models.WebauthnCredential
		var count int64
		if err := rows.Scan(&c.ID, &c.CredentialID, &c.PublicKey, &c.AAGUID,
			&count, &c.Transports, &c.Name, &c.BackupEligible, &c.BackupState,
			&c.Created, &c.LastUsed); err != nil {
			return nil, err
		}
		c.SignCount, err = checkedWebauthnSignCount(count)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddWebauthnCredential stores a newly registered passkey.
func (s *Store) AddWebauthnCredential(ctx context.Context, userID int64, c models.WebauthnCredential) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx,
		`INSERT INTO webauthn_credentials
		   (user_id, credential_id, public_key, aaguid, sign_count, transports, name, backup_eligible, backup_state)
		 SELECT id,$2,$3,$4,$5,$6,$7,$8,$9 FROM users WHERE id=$1 AND NOT disabled FOR SHARE`,
		userID, c.CredentialID, c.PublicKey, c.AAGUID, int64(c.SignCount), c.Transports, c.Name,
		c.BackupEligible, c.BackupState)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := addAuditTx(ctx, tx, "passkey_add", "user", strconv.FormatInt(userID, 10), c.Name); err != nil {
		return err
	}
	return tx.Commit()
}

// TouchWebauthnCredential updates the signature counter, current backup state
// and last-used time after a successful login. Backup-State (BS) can change over
// a credential's life (it flips when the authenticator first syncs), so the
// latest value is tracked here for the next assertion; the counter is kept for
// clone-detection hygiene.
func (s *Store) TouchWebauthnCredential(ctx context.Context, credentialID []byte, signCount uint32, backupState bool) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webauthn_credentials SET sign_count=$1, backup_state=$2, last_used_at=now() WHERE credential_id=$3`,
		int64(signCount), backupState, credentialID)
	return err
}

// DeleteWebauthnCredential removes one of the user's passkeys and returns its name,
// so the caller can name it in the audit trail. ErrNotFound when no such credential
// belongs to the user.
func (s *Store) DeleteWebauthnCredential(ctx context.Context, userID, id int64) (name string, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	err = tx.QueryRowContext(ctx,
		`DELETE FROM webauthn_credentials WHERE id=$1 AND user_id=$2 RETURNING name`, id, userID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if err := addAuditTx(ctx, tx, "passkey_delete", "user", strconv.FormatInt(userID, 10), name); err != nil {
		return "", err
	}
	return name, tx.Commit()
}
