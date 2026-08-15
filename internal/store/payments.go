package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// AddPayment records a dated payment a neighbor made toward a billing year.
// It is allowed regardless of the year's status (the payment side is decoupled
// from booking lock).
func (s *Store) AddPayment(ctx context.Context, yearID, neighborID int64, amount decimal.Decimal, paidOn time.Time, note string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note)
		 VALUES ($1,$2,$3,$4,$5)`,
		yearID, neighborID, amount, paidOn, note)
	return err
}

// ListPayments returns a neighbor's payments for a year, oldest first.
func (s *Store) ListPayments(ctx context.Context, yearID, neighborID int64) ([]models.Payment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, billing_year_id, neighbor_id, amount, paid_on, note, created_at
		   FROM payments WHERE billing_year_id=$1 AND neighbor_id=$2 AND deleted_at IS NULL
		  ORDER BY paid_on, id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Payment
	for rows.Next() {
		var p models.Payment
		if err := rows.Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.Amount, &p.PaidOn, &p.Note, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPayment returns a single payment, e.g. to scope a delete and redirect back
// to the right neighbor/year.
func (s *Store) GetPayment(ctx context.Context, id int64) (models.Payment, error) {
	var p models.Payment
	err := s.db.QueryRowContext(ctx,
		`SELECT id, billing_year_id, neighbor_id, amount, paid_on, note, created_at
		   FROM payments WHERE id=$1`, id).
		Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.Amount, &p.PaidOn, &p.Note, &p.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// CountPaymentsForNeighborYear returns how many payments a neighbor has in a
// year — used to block removing the neighbor from the year while payments exist
// (which would orphan the rows and silently drop the year's paid total).
func (s *Store) CountPaymentsForNeighborYear(ctx context.Context, yearID, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM payments WHERE billing_year_id=$1 AND neighbor_id=$2 AND deleted_at IS NULL`,
		yearID, neighborID).Scan(&n)
	return n, err
}

// DeletePayment soft-deletes a payment (sets deleted_at), so an accidental delete
// can be undone. It drops out of every sum/list immediately; a background purge
// removes it for good after the grace window.
func (s *Store) DeletePayment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE payments SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`, id)
	return err
}

// RestorePayment reverses a soft-delete (undo). No-op if already active or gone.
func (s *Store) RestorePayment(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE payments SET deleted_at=NULL WHERE id=$1 AND deleted_at IS NOT NULL`, id)
	return err
}

// PurgeDeletedPayments hard-deletes payments soft-deleted before the cutoff.
func (s *Store) PurgeDeletedPayments(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM payments WHERE deleted_at IS NOT NULL AND deleted_at < $1`, before)
	return err
}

// NeighborPaymentSum returns the total a neighbor has paid toward a year.
func (s *Store) NeighborPaymentSum(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM payments
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND deleted_at IS NULL`, yearID, neighborID).Scan(&sum)
	return sum, err
}

// BillingYearIDForYear returns the id of the billing year with the given year
// number, or ErrNotFound. Used by the optional carry-forward to find next year.
func (s *Store) BillingYearIDForYear(ctx context.Context, year int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM billing_years WHERE year=$1`, year).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// CarryForward moves an open balance from one year to the next in a single
// transaction: a −amount transfer-out ledger posting settles the source year
// (its remaining goes to 0) and a +amount opening posting seeds the target year.
// Bypasses the completed-year gate deliberately — this is a settlement action.
func (s *Store) CarryForward(ctx context.Context, neighborID, fromYearID, toYearID int64, amount decimal.Decimal, when time.Time, fromDesc, toDesc string) error {
	tid, err := randToken()
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Both sides share transfer_id so the pair reverses atomically (see
	// DeleteLedgerTransfer / SetLedgerVoidedTransfer).
	const ins = `INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date, transfer_id)
	             VALUES ($1,$2,$3,$4,$5,$6)`
	if _, err := tx.ExecContext(ctx, ins, fromYearID, neighborID, amount.Neg(), fromDesc, when, tid); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, ins, toYearID, neighborID, amount, toDesc, when, tid); err != nil {
		return err
	}
	return tx.Commit()
}

// randToken returns a random 128-bit hex id (used to link transfer postings).
func randToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
