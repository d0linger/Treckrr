package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

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

// SeenPaymentHashes reports which of the given hashes were already imported —
// one query for the whole statement instead of one EXISTS per transaction.
func (s *Store) SeenPaymentHashes(ctx context.Context, hashes []string) (map[string]bool, error) {
	out := make(map[string]bool, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT hash FROM payment_imports WHERE hash = ANY($1)`, hashes)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out[h] = true
	}
	return out, rows.Err()
}

// InvoiceRefTarget is one issued invoice as a bank-import match target: enough
// to book a credit against it, plus the reference and the payer-side name.
type InvoiceRefTarget struct {
	ID               int64
	YearID           int64
	NeighborID       int64
	Number           string
	PaymentReference string
	NeighborName     string
}

// IssuedInvoiceTargets returns EVERY issued invoice with year, neighbor,
// reference and neighbor name — the whole match table in one query. The
// per-transaction path (a regex query per credit, plus a neighbor-name SELECT
// per match) cost a 400-line statement ~1600 round trips per preview and the
// same again on commit.
func (s *Store) IssuedInvoiceTargets(ctx context.Context) ([]InvoiceRefTarget, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT iv.id, iv.billing_year_id, iv.neighbor_id, iv.number,
		        COALESCE(iv.payment_reference, ''), n.name
		   FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		  WHERE iv.kind = 'invoice' AND iv.status = 'issued'
		  ORDER BY iv.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InvoiceRefTarget
	for rows.Next() {
		var t InvoiceRefTarget
		if err := rows.Scan(&t.ID, &t.YearID, &t.NeighborID, &t.Number, &t.PaymentReference, &t.NeighborName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NeighborIBANMap maps every active neighbor's normalized IBAN to their id.
// An IBAN carried by SEVERAL neighbors maps to 0 — an ambiguous account must
// never auto-book against an arbitrary one of them (same rule as the old
// per-transaction NeighborIDByIBAN, whose upper(replace(...)) predicate no
// index could serve, one sequential scan of neighbors per credit).
func (s *Store) NeighborIBANMap(ctx context.Context) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT upper(replace(iban, ' ', '')), CASE WHEN count(*) = 1 THEN min(id) ELSE 0 END
		   FROM neighbors
		  WHERE NOT archived AND btrim(iban) <> ''
		  GROUP BY upper(replace(iban, ' ', ''))`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var iban string
		var id int64
		if err := rows.Scan(&iban, &id); err != nil {
			return nil, err
		}
		out[iban] = id
	}
	return out, rows.Err()
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
