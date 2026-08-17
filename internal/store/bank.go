package store

import (
	"context"
	"database/sql"
	"errors"

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
		    AND position(payment_reference in $1) > 0
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

// RecordPaymentImport marks a bank transaction hash as imported. Returns true only
// if it was newly recorded (i.e. not seen before), so the caller books the payment
// exactly once even across re-imports.
func (s *Store) RecordPaymentImport(ctx context.Context, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO payment_imports (hash) VALUES ($1) ON CONFLICT (hash) DO NOTHING`, hash)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
