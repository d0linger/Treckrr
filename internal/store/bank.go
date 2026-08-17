package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// InvoiceByReferenceText finds the issued invoice whose payment_reference (= its
// number) appears in the bank remittance text, so an incoming credit can be matched
// to the neighbor+year to settle. Longest reference wins if several match. Returns
// (nil, nil) when nothing matches.
func (s *Store) InvoiceByReferenceText(ctx context.Context, text string) (*models.Invoice, error) {
	iv, err := scanInvoice(s.db.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE kind='invoice' AND status='issued' AND payment_reference <> ''
		    AND $1 ~ ('(^|[^0-9A-Za-z])' || payment_reference || '([^0-9A-Za-z]|$)')
		  ORDER BY length(payment_reference) DESC LIMIT 1`, text))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &iv, nil
}

// PaymentImportSeen reports whether a bank transaction hash was already imported.
func (s *Store) PaymentImportSeen(ctx context.Context, hash string) (bool, error) {
	var seen bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM payment_imports WHERE hash=$1)`, hash).Scan(&seen)
	return seen, err
}

// ImportPayment books a bank credit and records its de-dup hash ATOMICALLY, in one
// transaction: it inserts the hash (ON CONFLICT DO NOTHING) and — only if that hash
// was new — inserts the payment. Returns (true, nil) when a payment was booked,
// (false, nil) when the credit was already imported. This closes the lost-payment
// window where a marked-imported hash could survive a failed AddPayment and skip
// the credit forever.
func (s *Store) ImportPayment(ctx context.Context, hash string, yearID, neighborID int64, amount decimal.Decimal, paidOn time.Time, note string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO payment_imports (hash) VALUES ($1) ON CONFLICT (hash) DO NOTHING`, hash)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, tx.Commit() // already imported → nothing to book
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note)
		 VALUES ($1,$2,$3,$4,$5)`, yearID, neighborID, amount, paidOn, note); err != nil {
		return false, err // rollback also undoes the hash insert
	}
	return true, tx.Commit()
}
