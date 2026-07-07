package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"

	"treckrr/internal/models"
)

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
		  WHERE id=$2 RETURNING webauthn_handle`, fresh, userID).Scan(&handle)
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
		`SELECT `+userCols+` FROM users WHERE webauthn_handle=$1`, handle))
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
		`SELECT id, credential_id, public_key, aaguid, sign_count, transports, name, created_at, last_used_at
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
			&count, &c.Transports, &c.Name, &c.Created, &c.LastUsed); err != nil {
			return nil, err
		}
		c.SignCount = uint32(count) //nolint:gosec // sign_count is a small non-negative counter
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddWebauthnCredential stores a newly registered passkey.
func (s *Store) AddWebauthnCredential(ctx context.Context, userID int64, c models.WebauthnCredential) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webauthn_credentials (user_id, credential_id, public_key, aaguid, sign_count, transports, name)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		userID, c.CredentialID, c.PublicKey, c.AAGUID, int64(c.SignCount), c.Transports, c.Name)
	return err
}

// TouchWebauthnCredential updates the signature counter and last-used time after
// a successful login (clone-detection hygiene).
func (s *Store) TouchWebauthnCredential(ctx context.Context, credentialID []byte, signCount uint32) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE webauthn_credentials SET sign_count=$1, last_used_at=now() WHERE credential_id=$2`,
		int64(signCount), credentialID)
	return err
}

// DeleteWebauthnCredential removes one of a user's passkeys.
func (s *Store) DeleteWebauthnCredential(ctx context.Context, userID, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM webauthn_credentials WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}
