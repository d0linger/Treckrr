package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Ratenplan (Ausbaukarte 43) -------------------------------------------
//
// Installments are PLANNED rows only. Money keeps flowing through payments; the
// UI derives each installment's state by comparing the paid sum against the
// cumulative plan, so there is no second money path to keep consistent.

// AddInstallment records one agreed installment.
func (s *Store) AddInstallment(ctx context.Context, yearID, neighborID int64, amount decimal.Decimal, dueOn time.Time, note string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockPersonalDataNeighbor(ctx, tx, neighborID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO payment_plans (billing_year_id, neighbor_id, due_on, amount, note)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		yearID, neighborID, dueOn, amount, note).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// DeleteInstallment removes one installment and returns the deleted row, so the
// handler can redirect back to the right neighbor+year and write a precise audit
// line. ErrNotFound when the id does not exist.
func (s *Store) DeleteInstallment(ctx context.Context, id int64) (*models.PaymentPlan, error) {
	var p models.PaymentPlan
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM payment_plans WHERE id=$1
		 RETURNING id, billing_year_id, neighbor_id, due_on, amount, note, created_at`, id).
		Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.DueOn, &p.Amount, &p.Note, &p.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListInstallments returns the plan for a neighbor's year in due order.
func (s *Store) ListInstallments(ctx context.Context, yearID, neighborID int64) ([]models.PaymentPlan, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, billing_year_id, neighbor_id, due_on, amount, note, created_at
		   FROM payment_plans WHERE billing_year_id=$1 AND neighbor_id=$2
		  ORDER BY due_on, id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PaymentPlan
	for rows.Next() {
		var p models.PaymentPlan
		if err := rows.Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.DueOn, &p.Amount, &p.Note, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
