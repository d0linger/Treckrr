package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/d0linger/treckrr/internal/models"
)

// BillingYearByNumber returns the year with its price basis populated.
func (s *Store) BillingYearByNumber(ctx context.Context, year int) (*models.BillingYear, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT y.id, y.year, y.base_id, y.label, y.status, y.created_at,
		       b.id, b.year, b.name, b.locked, b.created_at
		  FROM billing_years y JOIN price_bases b ON b.id=y.base_id
		 WHERE y.year=$1`, year)
	y, err := scanBillingYear(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &y, nil
}

// CarryYearNeighbors adds every active source-year member to the target. The
// conflict guard makes retries safe and preserves existing target membership.
func (s *Store) CarryYearNeighbors(ctx context.Context, sourceID, targetID int64) (int, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO billing_year_neighbors (billing_year_id, neighbor_id)
		SELECT $2, source.neighbor_id
		  FROM billing_year_neighbors source
		  JOIN neighbors n ON n.id=source.neighbor_id
		 WHERE source.billing_year_id=$1 AND NOT n.archived
		ON CONFLICT DO NOTHING`, sourceID, targetID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
