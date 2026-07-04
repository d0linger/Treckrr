package store

import (
	"context"
	"database/sql"
	"errors"

	"treckrr/internal/models"
)

// ---- Billing years (Abrechnungsjahre) -----------------------------------

// ListBillingYears returns all billing years (newest first) with their basis.
func (s *Store) ListBillingYears(ctx context.Context) ([]models.BillingYear, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT y.id, y.year, y.base_id, y.label, y.status, y.created_at,
		       b.id, b.year, b.name, b.locked, b.created_at
		  FROM billing_years y
		  JOIN price_bases b ON b.id = y.base_id
		 ORDER BY y.year DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BillingYear
	for rows.Next() {
		y, err := scanBillingYear(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, y)
	}
	return out, rows.Err()
}

// GetBillingYear returns one billing year with its basis populated.
func (s *Store) GetBillingYear(ctx context.Context, id int64) (*models.BillingYear, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT y.id, y.year, y.base_id, y.label, y.status, y.created_at,
		       b.id, b.year, b.name, b.locked, b.created_at
		  FROM billing_years y
		  JOIN price_bases b ON b.id = y.base_id
		 WHERE y.id = $1`, id)
	y, err := scanBillingYear(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &y, nil
}

// LatestBillingYear returns the billing year with the highest year.
func (s *Store) LatestBillingYear(ctx context.Context) (*models.BillingYear, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT y.id, y.year, y.base_id, y.label, y.status, y.created_at,
		       b.id, b.year, b.name, b.locked, b.created_at
		  FROM billing_years y
		  JOIN price_bases b ON b.id = y.base_id
		 ORDER BY y.year DESC LIMIT 1`)
	y, err := scanBillingYear(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &y, nil
}

// CreateBillingYear inserts a new billing year bound to a basis.
func (s *Store) CreateBillingYear(ctx context.Context, year int, baseID int64, label string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO billing_years (year, base_id, label) VALUES ($1,$2,$3) RETURNING id`,
		year, baseID, label).Scan(&id)
	return id, err
}

// UpdateBillingYear changes the basis and label of a billing year.
func (s *Store) UpdateBillingYear(ctx context.Context, id, baseID int64, label string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE billing_years SET base_id=$1, label=$2 WHERE id=$3`, baseID, label, id)
	return err
}

// DeleteBillingYear removes a billing year and its entries/memberships.
func (s *Store) DeleteBillingYear(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM billing_years WHERE id=$1`, id)
	return err
}

// SetYearStatus sets the workflow status of a billing year.
func (s *Store) SetYearStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE billing_years SET status=$1 WHERE id=$2`, status, id)
	return err
}

// SetNeighborPaid marks a neighbor's yearly bill as paid or open.
func (s *Store) SetNeighborPaid(ctx context.Context, yearID, neighborID int64, paid bool) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE billing_year_neighbors
		   SET paid = $3, paid_at = CASE WHEN $3 THEN now() ELSE NULL END
		 WHERE billing_year_id = $1 AND neighbor_id = $2`, yearID, neighborID, paid)
	return err
}

// ResetYearPayments sets every neighbor of a year back to "open" (unpaid).
func (s *Store) ResetYearPayments(ctx context.Context, yearID int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE billing_year_neighbors SET paid = FALSE, paid_at = NULL WHERE billing_year_id = $1`, yearID)
	return err
}

// YearPayments returns a map of neighbor id -> paid flag for a billing year.
func (s *Store) YearPayments(ctx context.Context, yearID int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT neighbor_id, paid FROM billing_year_neighbors WHERE billing_year_id=$1`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var nid int64
		var paid bool
		if err := rows.Scan(&nid, &paid); err != nil {
			return nil, err
		}
		out[nid] = paid
	}
	return out, rows.Err()
}

// CountEntriesForYear returns the number of entries booked in a billing year.
func (s *Store) CountEntriesForYear(ctx context.Context, yearID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM entries WHERE billing_year_id=$1`, yearID).Scan(&n)
	return n, err
}

// PreviousBillingYear returns the billing year with the highest year strictly
// below the given year, or ErrNotFound when there is none.
func (s *Store) PreviousBillingYear(ctx context.Context, beforeYear int) (*models.BillingYear, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT y.id, y.year, y.base_id, y.label, y.status, y.created_at,
		       b.id, b.year, b.name, b.locked, b.created_at
		  FROM billing_years y
		  JOIN price_bases b ON b.id = y.base_id
		 WHERE y.year < $1
		 ORDER BY y.year DESC LIMIT 1`, beforeYear)
	y, err := scanBillingYear(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &y, nil
}

// ---- Year <-> neighbor membership --------------------------------------

// ListYearNeighbors returns the neighbors participating in a billing year.
func (s *Store) ListYearNeighbors(ctx context.Context, yearID int64) ([]models.Neighbor, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.name, n.note, n.archived, n.created_at
		  FROM billing_year_neighbors byn
		  JOIN neighbors n ON n.id = byn.neighbor_id
		 WHERE byn.billing_year_id = $1
		 ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNeighborRows(rows)
}

// ListNeighborsNotInYear returns active neighbors not yet in a billing year.
// Archived neighbors are not offered for new assignments.
func (s *Store) ListNeighborsNotInYear(ctx context.Context, yearID int64) ([]models.Neighbor, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.name, n.note, n.archived, n.created_at
		  FROM neighbors n
		 WHERE n.archived = FALSE
		   AND NOT EXISTS (
		     SELECT 1 FROM billing_year_neighbors byn
		      WHERE byn.neighbor_id = n.id AND byn.billing_year_id = $1)
		 ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNeighborRows(rows)
}

func scanNeighborRows(rows *sql.Rows) ([]models.Neighbor, error) {
	var out []models.Neighbor
	for rows.Next() {
		var n models.Neighbor
		if err := rows.Scan(&n.ID, &n.Name, &n.Note, &n.Archived, &n.Created); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNeighborToYear links a neighbor to a billing year (idempotent).
func (s *Store) AddNeighborToYear(ctx context.Context, yearID, neighborID int64) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO billing_year_neighbors (billing_year_id, neighbor_id)
		 VALUES ($1,$2) ON CONFLICT DO NOTHING`, yearID, neighborID)
	return err
}

// RemoveNeighborFromYear unlinks a neighbor from a billing year.
func (s *Store) RemoveNeighborFromYear(ctx context.Context, yearID, neighborID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM billing_year_neighbors WHERE billing_year_id=$1 AND neighbor_id=$2`,
		yearID, neighborID)
	return err
}

// NeighborInYear reports whether a neighbor participates in a billing year.
func (s *Store) NeighborInYear(ctx context.Context, yearID, neighborID int64) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM billing_year_neighbors
		   WHERE billing_year_id=$1 AND neighbor_id=$2)`, yearID, neighborID).Scan(&exists)
	return exists, err
}

// CountEntriesForNeighborYear returns entries a neighbor has in a year.
func (s *Store) CountEntriesForNeighborYear(ctx context.Context, yearID, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM entries WHERE billing_year_id=$1 AND neighbor_id=$2`,
		yearID, neighborID).Scan(&n)
	return n, err
}

func scanBillingYear(sc scanner) (models.BillingYear, error) {
	var (
		y models.BillingYear
		b models.PriceBase
	)
	if err := sc.Scan(&y.ID, &y.Year, &y.BaseID, &y.Label, &y.Status, &y.Created,
		&b.ID, &b.Year, &b.Name, &b.Locked, &b.Created); err != nil {
		return y, err
	}
	y.Base = &b
	return y, nil
}
