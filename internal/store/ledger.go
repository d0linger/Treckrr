package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// ListNeighborLedger returns a neighbour's manual account postings for a year,
// oldest first.
func (s *Store) ListNeighborLedger(ctx context.Context, yearID, neighborID int64) ([]models.LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount, description, created_at
		   FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND neighbor_id=$2
		  ORDER BY created_at, id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LedgerEntry
	for rows.Next() {
		var e models.LedgerEntry
		if err := rows.Scan(&e.ID, &e.Amount, &e.Description, &e.Created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NeighborLedgerSum returns the signed sum of a neighbour's ledger for a year
// (positive = extra receivable, negative = payable).
func (s *Store) NeighborLedgerSum(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND neighbor_id=$2`, yearID, neighborID).Scan(&sum)
	return sum, err
}

// AddNeighborLedger records a manual posting and returns its id.
func (s *Store) AddNeighborLedger(ctx context.Context, yearID, neighborID int64, amount decimal.Decimal, description string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description)
		 VALUES ($1,$2,$3,$4) RETURNING id`, yearID, neighborID, amount, description).Scan(&id)
	return id, err
}

// GetLedgerEntry returns a posting's owning year/neighbour plus amount and
// description (used to authorise, lock-check and audit a delete before it runs).
func (s *Store) GetLedgerEntry(ctx context.Context, id int64) (yearID, neighborID int64, amount decimal.Decimal, description string, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT billing_year_id, neighbor_id, amount, description
		   FROM neighbor_ledger WHERE id=$1`, id).Scan(&yearID, &neighborID, &amount, &description)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// DeleteNeighborLedger removes a posting.
func (s *Store) DeleteNeighborLedger(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM neighbor_ledger WHERE id=$1`, id)
	return err
}
