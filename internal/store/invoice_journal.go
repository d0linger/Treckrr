package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Rechnungsjournal (Ausbaukarte 47/50/52/55) ----------------------------

// JournalRow is one issued document in the per-year journal: exactly the
// Netto/USt/Brutto line accounting asks for. Legacy rows without a snapshot
// carry zero amounts (COALESCE) rather than being hidden.
type JournalRow struct {
	ID           int64
	Number       string
	Kind         string // invoice / storno / gutschrift
	Status       string // issued / canceled
	IssuedOn     time.Time
	NeighborID   int64
	NeighborName string
	Net          decimal.Decimal
	VATRate      decimal.Decimal
	VATAmount    decimal.Decimal
	Gross        decimal.Decimal
}

// CountsForRevenue reports whether the row belongs into a signed revenue sum.
// The rule: every kind='invoice' row counts (a canceled original is always
// paired with its issued Storno, so the pair nets to zero), and any other kind
// only while issued (a Gutschrift canceled by a full Storno cascade has no
// reversal document of its own — including it would subtract twice).
func (r JournalRow) CountsForRevenue() bool {
	return r.Kind == "invoice" || r.Status == "issued"
}

// ListInvoiceJournal returns every invoice-family document of a year in
// chronological order.
func (s *Store) ListInvoiceJournal(ctx context.Context, yearID int64) ([]JournalRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT iv.id, iv.number, iv.kind, iv.status, iv.issued_on, iv.neighbor_id, n.name,
		        COALESCE(iv.net,0), COALESCE(iv.vat_rate,0), COALESCE(iv.vat_amount,0), COALESCE(iv.gross,0)
		   FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		  WHERE iv.billing_year_id = $1
		  ORDER BY iv.issued_on, iv.id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalRow
	for rows.Next() {
		var j JournalRow
		if err := rows.Scan(&j.ID, &j.Number, &j.Kind, &j.Status, &j.IssuedOn, &j.NeighborID, &j.NeighborName,
			&j.Net, &j.VATRate, &j.VATAmount, &j.Gross); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListInvoiceDocs returns the full documents of a year (for the archive
// export), with legacy rows hydrated from live data where possible so their
// PDF can still be rendered. A row whose snapshot cannot be reconstructed is
// returned with Content nil — the caller decides how to report it.
func (s *Store) ListInvoiceDocs(ctx context.Context, yearID int64) ([]models.Invoice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices WHERE billing_year_id=$1 ORDER BY issued_on, id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Invoice
	for rows.Next() {
		iv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Content == nil {
			if c, err := s.contentOrBuild(ctx, out[i], out[i].BillingYearID, out[i].NeighborID); err == nil {
				out[i].Content = &c
			}
		}
	}
	return out, nil
}

// KUCalendarYearGross is the signed revenue of one CALENDAR year across all
// billing years — the figure the Kleinunternehmer ceiling (§ 6 Abs 1 Z 27
// UStG) is measured against. Same counting rule as CountsForRevenue.
func (s *Store) KUCalendarYearGross(ctx context.Context, calYear int) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(gross),0) FROM invoices
		  WHERE EXTRACT(YEAR FROM issued_on) = $1
		    AND (kind='invoice' OR status='issued')`, calYear).Scan(&sum)
	return sum, err
}
