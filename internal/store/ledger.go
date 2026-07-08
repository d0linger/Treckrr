package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// ListNeighborLedger returns a neighbour's manual account postings for a year,
// oldest first. Voided postings are included (shown struck-through) but do not
// count toward the balance.
func (s *Store) ListNeighborLedger(ctx context.Context, yearID, neighborID int64) ([]models.LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount, description, posting_date, voided, void_reason, created_at
		   FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND neighbor_id=$2
		  ORDER BY posting_date, id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LedgerEntry
	for rows.Next() {
		var e models.LedgerEntry
		if err := rows.Scan(&e.ID, &e.Amount, &e.Description, &e.Date, &e.Voided, &e.VoidReason, &e.Created); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NeighborLedgerSum returns the signed sum of a neighbour's non-voided ledger
// for a year (positive = extra receivable, negative = payable).
func (s *Store) NeighborLedgerSum(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND NOT voided`, yearID, neighborID).Scan(&sum)
	return sum, err
}

// YearLedgerSum returns the signed sum of all non-voided ledger postings for a
// year (used to net the statistics result).
func (s *Store) YearLedgerSum(ctx context.Context, yearID int64) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND NOT voided`, yearID).Scan(&sum)
	return sum, err
}

// AddNeighborLedger records a manual posting and returns its id.
func (s *Store) AddNeighborLedger(ctx context.Context, yearID, neighborID int64, amount decimal.Decimal, description string, date time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`, yearID, neighborID, amount, description, date).Scan(&id)
	return id, err
}

// UpdateNeighborLedger edits a posting's amount, description and date.
func (s *Store) UpdateNeighborLedger(ctx context.Context, id int64, amount decimal.Decimal, description string, date time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbor_ledger SET amount=$1, description=$2, posting_date=$3 WHERE id=$4`,
		amount, description, date, id)
	return err
}

// SetLedgerVoided marks a posting as voided (or restores it).
func (s *Store) SetLedgerVoided(ctx context.Context, id int64, voided bool, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbor_ledger SET voided=$1, void_reason=$2 WHERE id=$3`, voided, reason, id)
	return err
}

// GetLedgerEntry returns a posting with its owning year/neighbour (used to
// authorise, lock-check, prefill an edit form, and audit).
func (s *Store) GetLedgerEntry(ctx context.Context, id int64) (yearID, neighborID int64, e models.LedgerEntry, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT billing_year_id, neighbor_id, id, amount, description, posting_date, voided, void_reason, created_at
		   FROM neighbor_ledger WHERE id=$1`, id).
		Scan(&yearID, &neighborID, &e.ID, &e.Amount, &e.Description, &e.Date, &e.Voided, &e.VoidReason, &e.Created)
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
