package store

import (
	"context"
	"database/sql"
	"errors"
)

// lockPersonalDataNeighbor serializes live personal-data writes with erasure.
// Callers holding this lock must not subsequently acquire year/account locks:
// monetary writers use lockAccountBoundary's year-before-neighbor order instead.
func lockPersonalDataNeighbor(ctx context.Context, tx *sql.Tx, neighborID int64) error {
	var anonymized bool
	err := tx.QueryRowContext(ctx,
		`SELECT anonymized FROM neighbors WHERE id=$1 FOR NO KEY UPDATE`, neighborID).Scan(&anonymized)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if anonymized {
		return ErrNeighborAnonymized
	}
	return nil
}
