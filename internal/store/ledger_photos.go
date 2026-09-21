package store

import (
	"context"
	"database/sql"
	"errors"
)

// lockLedgerPhotoBooking freezes receipt writes with their structured booking.
func lockLedgerPhotoBooking(ctx context.Context, tx *sql.Tx, ledgerID int64) error {
	yearID, neighborID, err := ledgerAccount(ctx, tx, ledgerID)
	if err != nil {
		return err
	}
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM neighbor_ledger
		WHERE id=$1 AND booking IS NOT NULL AND transfer_id='' FOR UPDATE`, ledgerID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// AddLedgerPhoto stores re-encoded evidence without creating an outgoing entry.
func (s *Store) AddLedgerPhoto(ctx context.Context, ledgerID int64, image []byte, contentType string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockLedgerPhotoBooking(ctx, tx, ledgerID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO ledger_photos (ledger_id,image,content_type) VALUES ($1,$2,$3) RETURNING id`,
		ledgerID, image, contentType).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ListLedgerPhotos lists only metadata for the requested incoming booking.
func (s *Store) ListLedgerPhotos(ctx context.Context, ledgerID int64) ([]EntryPhoto, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,created_at FROM ledger_photos WHERE ledger_id=$1 ORDER BY created_at DESC,id DESC`, ledgerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntryPhoto{}
	for rows.Next() {
		var p EntryPhoto
		if err := rows.Scan(&p.ID, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetLedgerPhoto scopes photo IDs to their ledger booking, preventing cross-links.
func (s *Store) GetLedgerPhoto(ctx context.Context, ledgerID, photoID int64) ([]byte, string, error) {
	var image []byte
	var contentType string
	err := s.db.QueryRowContext(ctx,
		`SELECT image,content_type FROM ledger_photos WHERE ledger_id=$1 AND id=$2`, ledgerID, photoID).
		Scan(&image, &contentType)
	return image, contentType, err
}

// DeleteLedgerPhoto removes evidence only while its account remains mutable.
func (s *Store) DeleteLedgerPhoto(ctx context.Context, ledgerID, photoID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockLedgerPhotoBooking(ctx, tx, ledgerID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM ledger_photos WHERE ledger_id=$1 AND id=$2`, ledgerID, photoID)
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
	return tx.Commit()
}

// LedgerPhotoCounts counts evidence for exactly the visible ledger IDs.
func (s *Store) LedgerPhotoCounts(ctx context.Context, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT ledger_id,count(*) FROM ledger_photos WHERE ledger_id=ANY($1) GROUP BY ledger_id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		out[id] = count
	}
	return out, rows.Err()
}
