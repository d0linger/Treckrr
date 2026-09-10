package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// AddPayment records a dated payment a neighbor made toward a billing year.
// It is allowed regardless of the year's status (the payment side is decoupled
// from booking lock).
func (s *Store) AddPayment(ctx context.Context, yearID, neighborID int64, amount decimal.Decimal, paidOn time.Time, note, method string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, false, false)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	// Linked to the ACTIVE invoice at recording time, forward-only: after a
	// storno + re-issue the attribution used to be guesswork. A scalar subquery
	// keeps this a single statement — no invoice means NULL, exactly as before.
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note, method, invoice_id)
		 VALUES ($1,$2,$3,$4,$5,$6,
		         (SELECT id FROM invoices
		           WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued'
		           ORDER BY id DESC LIMIT 1))
		 RETURNING id`,
		yearID, neighborID, amount, paidOn, note, method).Scan(&id)
	if err != nil {
		return err
	}
	if err := addAuditTx(
		ctx,
		tx,
		"payment_add",
		"payment",
		strconv.FormatInt(id, 10),
		paymentAuditState(amount, paidOn, method),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// UpdatePayment corrects a payment's amount, date, note and method in place —
// the alternative was delete-and-retype, which loses the created_at ordering
// and, with it, any sense of when the money actually arrived.
// Reports whether a row was actually changed: the WHERE excludes soft-deleted
// payments, and GetPayment (by id) still returns them, so a caller that only
// checked the error would audit and report a change that never happened.
func (s *Store) UpdatePayment(ctx context.Context, id int64, amount decimal.Decimal, paidOn time.Time, note, method string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := paymentAccount(ctx, tx, id)
	if err != nil {
		return false, err
	}
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, false, false)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrNotFound
	}
	before, err := paymentForUpdate(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if before.DeletedAt != nil {
		return false, tx.Commit()
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE payments SET amount=$2, paid_on=$3, note=$4, method=$5 WHERE id=$1 AND deleted_at IS NULL`,
		id, amount, paidOn, note, method)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	detail := "before{" + paymentAuditState(before.Amount, before.PaidOn, before.Method) + "} after{" +
		paymentAuditState(amount, paidOn, method) + "}"
	if err := addAuditTx(
		ctx,
		tx,
		"payment_update",
		"payment",
		strconv.FormatInt(id, 10),
		detail,
	); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

type paymentMutationRow struct {
	BillingYearID, NeighborID int64
	Amount                    decimal.Decimal
	PaidOn                    time.Time
	Method                    string
	DeletedAt                 *time.Time
}

func paymentAccount(ctx context.Context, tx *sql.Tx, id int64) (yearID, neighborID int64, err error) {
	err = tx.QueryRowContext(ctx,
		`SELECT billing_year_id, neighbor_id FROM payments WHERE id=$1`, id).Scan(&yearID, &neighborID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func paymentForUpdate(ctx context.Context, tx *sql.Tx, id int64) (paymentMutationRow, error) {
	var row paymentMutationRow
	var deleted sql.NullTime
	err := tx.QueryRowContext(ctx, `
		SELECT billing_year_id, neighbor_id, amount, paid_on, method, deleted_at
		  FROM payments WHERE id=$1 FOR UPDATE`, id).Scan(
		&row.BillingYearID,
		&row.NeighborID,
		&row.Amount,
		&row.PaidOn,
		&row.Method,
		&deleted,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil {
		return row, err
	}
	if deleted.Valid {
		row.DeletedAt = &deleted.Time
	}
	return row, nil
}

func paymentAuditState(amount decimal.Decimal, paidOn time.Time, method string) string {
	return fmt.Sprintf(
		"amount=%s; paid_on=%s; method=%q",
		amount.StringFixed(2),
		paidOn.Format("2006-01-02"),
		method,
	)
}

// ListPayments returns a neighbor's payments for a year, oldest first.
func (s *Store) ListPayments(ctx context.Context, yearID, neighborID int64) ([]models.Payment, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.billing_year_id, p.neighbor_id, p.amount, p.paid_on, p.note,
		        p.method, p.invoice_id, COALESCE(iv.number, ''), p.created_at
		   FROM payments p LEFT JOIN invoices iv ON iv.id = p.invoice_id
		  WHERE p.billing_year_id=$1 AND p.neighbor_id=$2 AND p.deleted_at IS NULL
		  ORDER BY p.paid_on, p.id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Payment
	for rows.Next() {
		var p models.Payment
		if err := rows.Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.Amount, &p.PaidOn, &p.Note,
			&p.Method, &p.InvoiceID, &p.InvoiceNumber, &p.Created); err != nil {
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
		`SELECT p.id, p.billing_year_id, p.neighbor_id, p.amount, p.paid_on, p.note,
		        p.method, p.invoice_id, COALESCE(iv.number, ''), p.created_at
		   FROM payments p LEFT JOIN invoices iv ON iv.id = p.invoice_id WHERE p.id=$1`, id).
		Scan(&p.ID, &p.BillingYearID, &p.NeighborID, &p.Amount, &p.PaidOn, &p.Note,
			&p.Method, &p.InvoiceID, &p.InvoiceNumber, &p.Created)
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

// DeletePayment soft-deletes a payment. Returns true only when an active row was
// actually deleted; false (no error) if it was already deleted or gone, so the
// caller can skip a misleading success flash + audit event. The retained row is
// durable financial evidence until a reviewed retention policy says otherwise.
func (s *Store) DeletePayment(ctx context.Context, id int64) (bool, error) {
	return s.setPaymentDeleted(ctx, id, true)
}

func (s *Store) setPaymentDeleted(ctx context.Context, id int64, deleted bool) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := paymentAccount(ctx, tx, id)
	if err != nil {
		return false, err
	}
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, false, false)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrNotFound
	}
	payment, err := paymentForUpdate(ctx, tx, id)
	if err != nil {
		return false, err
	}
	wantChange := (deleted && payment.DeletedAt == nil) || (!deleted && payment.DeletedAt != nil)
	if !wantChange {
		return false, tx.Commit()
	}
	query := `UPDATE payments SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`
	action := "payment_delete"
	if !deleted {
		query = `UPDATE payments SET deleted_at=NULL WHERE id=$1 AND deleted_at IS NOT NULL`
		action = "payment_restore"
	}
	res, err := tx.ExecContext(ctx, query, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err := addAuditTx(
		ctx,
		tx,
		action,
		"payment",
		strconv.FormatInt(id, 10),
		paymentAuditState(payment.Amount, payment.PaidOn, payment.Method),
	); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// RestorePayment reverses a soft-delete (undo). Returns true only when a
// soft-deleted row was actually reactivated; false (no error) if it was already
// active or gone, so the caller can skip a misleading success flash + audit event.
func (s *Store) RestorePayment(ctx context.Context, id int64) (bool, error) {
	return s.setPaymentDeleted(ctx, id, false)
}

// PurgeDeletedPayments intentionally keeps durable financial evidence. The
// seven-day UI undo window is not a retention policy; deletion requires a
// separately reviewed legal-hold-aware retention decision.
func (s *Store) PurgeDeletedPayments(ctx context.Context, before time.Time) error {
	return nil
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
	if err := lockSettlementAccounts(
		ctx,
		tx,
		accountKey{yearID: fromYearID, neighborID: neighborID},
		accountKey{yearID: toYearID, neighborID: neighborID},
	); err != nil {
		return err
	}
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
	if err := addAuditTx(
		ctx,
		tx,
		"carry_forward",
		"ledger_transfer",
		tid,
		fmt.Sprintf("amount=%s; from_year_id=%d; to_year_id=%d", amount.StringFixed(2), fromYearID, toYearID),
	); err != nil {
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

// PaymentRow is one payment with the billing year it belongs to, for the
// cross-year history (Ausbaukarte 79).
type PaymentRow struct {
	models.Payment
	Year int
}

// ListNeighborPayments returns every payment a neighbor ever made, newest
// first — the "Zahlungshistorie" the overview page promised but never showed.
func (s *Store) ListNeighborPayments(ctx context.Context, neighborID int64) ([]PaymentRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.billing_year_id, p.neighbor_id, p.amount, p.paid_on, p.note,
		        p.method, p.invoice_id, COALESCE(iv.number, ''), p.created_at, y.year
		   FROM payments p
		   JOIN billing_years y ON y.id = p.billing_year_id
		   LEFT JOIN invoices iv ON iv.id = p.invoice_id
		  WHERE p.neighbor_id = $1 AND p.deleted_at IS NULL
		  ORDER BY p.paid_on DESC, p.id DESC`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PaymentRow
	for rows.Next() {
		var r PaymentRow
		if err := rows.Scan(&r.ID, &r.BillingYearID, &r.NeighborID, &r.Amount, &r.PaidOn, &r.Note,
			&r.Method, &r.InvoiceID, &r.InvoiceNumber, &r.Created, &r.Year); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
