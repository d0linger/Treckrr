package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// CreateWebauthnCeremony persists the opaque begin→finish ceremony session under a
// random id with a hard expiry (SH-03).
func (s *Store) CreateWebauthnCeremony(ctx context.Context, id string, data []byte, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO webauthn_ceremonies (id, session_data, expires_at) VALUES ($1,$2,$3)`,
		id, data, expiresAt)
	return err
}

// ConsumeWebauthnCeremony atomically fetches and deletes the ceremony, so it is
// strictly single-use: a replayed finish (or an expired ceremony) finds no row and
// gets ErrNotFound (SH-03).
func (s *Store) ConsumeWebauthnCeremony(ctx context.Context, id string) ([]byte, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM webauthn_ceremonies WHERE id=$1 AND expires_at > now() RETURNING session_data`,
		id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return data, err
}

// PurgeExpiredWebauthnCeremonies removes ceremonies past their expiry (abandoned
// begins that were never finished). Called on the maintenance timer.
func (s *Store) PurgeExpiredWebauthnCeremonies(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE expires_at <= now()`)
	return err
}
