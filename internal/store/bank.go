package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
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

// NeighborIDByIBAN finds the active neighbor whose stored IBAN matches the
// payer account, comparing both sides normalized (no spaces, upper case).
// Returns 0 when no or several neighbors carry the IBAN — an ambiguous account
// must never auto-book against an arbitrary one of them.
func (s *Store) NeighborIDByIBAN(ctx context.Context, iban string) (int64, error) {
	norm := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(iban), " ", ""))
	if norm == "" {
		return 0, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT CASE WHEN count(*) = 1 THEN min(id) ELSE 0 END
		   FROM neighbors
		  WHERE NOT archived AND upper(replace(iban, ' ', '')) = $1`, norm).Scan(&id)
	return id, err
}

// LatestOpenInvoiceForNeighbor returns the neighbor's newest issued invoice, or
// (nil, nil) when none exists — the target an IBAN-matched credit books against.
func (s *Store) LatestOpenInvoiceForNeighbor(ctx context.Context, neighborID int64) (*models.Invoice, error) {
	iv, err := scanInvoice(s.db.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE neighbor_id=$1 AND kind='invoice' AND status='issued'
		  ORDER BY id DESC LIMIT 1`, neighborID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &iv, nil
}

// AssignableInvoice is one issued invoice offered for manual assignment in the
// bank-import preview.
type AssignableInvoice struct {
	ID           int64
	YearID       int64
	NeighborID   int64
	Number       string
	NeighborName string
	Gross        decimal.Decimal
}

// ListAssignableInvoices returns the issued invoices (newest first) a credit can
// be assigned to by hand when neither reference nor IBAN matched.
func (s *Store) ListAssignableInvoices(ctx context.Context) ([]AssignableInvoice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT iv.id, iv.billing_year_id, iv.neighbor_id, iv.number, n.name, COALESCE(iv.gross, 0)
		   FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		  WHERE iv.kind='invoice' AND iv.status='issued'
		  ORDER BY iv.id DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssignableInvoice
	for rows.Next() {
		var a AssignableInvoice
		if err := rows.Scan(&a.ID, &a.YearID, &a.NeighborID, &a.Number, &a.NeighborName, &a.Gross); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
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
func (s *Store) ImportPayment(ctx context.Context, hash string, yearID, neighborID, invoiceID int64, amount decimal.Decimal, paidOn time.Time, note string) (bool, error) {
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
	// A bank credit is by definition an Überweisung, and the matcher already
	// resolved the invoice — store both instead of re-deriving them.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note, method, invoice_id)
		 VALUES ($1,$2,$3,$4,$5,'überweisung',$6)`, yearID, neighborID, amount, paidOn, note, nullable(invoiceID)); err != nil {
		return false, err // rollback also undoes the hash insert
	}
	return true, tx.Commit()
}
