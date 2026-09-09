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
	Kind         string // invoice / storno / gutschrift / anzahlung
	Status       string // issued / canceled
	RefKind      string // kind of the document this one corrects ("" if none)
	IssuedOn     time.Time
	NeighborID   int64
	NeighborName string
	Net          decimal.Decimal
	VATRate      decimal.Decimal
	VATAmount    decimal.Decimal
	Gross        decimal.Decimal
}

// CountsForRevenue reports whether the row belongs into a signed revenue sum.
// Case by case, because "issued" alone is not the criterion:
//
//   - invoice: always. A canceled original is always paired with its own issued
//     Storno, so the pair nets to zero; dropping it would subtract twice.
//   - anzahlung: never. An Abschlag is a payment request, not a tax document —
//     the Schlussrechnung carries the revenue (see CreateAnzahlung).
//   - storno: only while issued, and only when it reverses a document that
//     itself counted. A Storno of an Anzahlung must not subtract revenue that
//     was never added.
//   - gutschrift: only while issued. One canceled by the full-Storno cascade
//     has no reversal document of its own, so it must simply drop out.
//
// KUCalendarYearGross re-states this rule in SQL; change both together.
func (r JournalRow) CountsForRevenue() bool {
	switch r.Kind {
	case "invoice":
		return true
	case "anzahlung":
		return false
	case "storno":
		return r.Status == "issued" && r.RefKind != "anzahlung"
	default:
		return r.Status == "issued"
	}
}

// ListInvoiceJournal returns every invoice-family document of a year in
// chronological order.
func (s *Store) ListInvoiceJournal(ctx context.Context, yearID int64) ([]JournalRow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT iv.id, iv.number, iv.kind, iv.status, COALESCE(ref.kind, ''), iv.issued_on, iv.neighbor_id, n.name,
		        COALESCE(iv.net,0), COALESCE(iv.vat_rate,0), COALESCE(iv.vat_amount,0), COALESCE(iv.gross,0)
		   FROM invoices iv
		   JOIN neighbors n ON n.id = iv.neighbor_id
		   LEFT JOIN invoices ref ON ref.id = iv.references_invoice_id
		  WHERE iv.billing_year_id = $1
		  ORDER BY iv.issued_on, iv.id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalRow
	for rows.Next() {
		var j JournalRow
		if err := rows.Scan(&j.ID, &j.Number, &j.Kind, &j.Status, &j.RefKind, &j.IssuedOn, &j.NeighborID, &j.NeighborName,
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
	// Mirrors JournalRow.CountsForRevenue in SQL, case for case, so the two
	// cannot drift: invoice always; anzahlung never; storno only while issued
	// and only when it reverses something that counted; everything else
	// (gutschrift) while issued. Keep this switch and that one in step — they
	// are the same rule, and the ceiling this feeds is a tax figure.
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(iv.gross),0)
		   FROM invoices iv LEFT JOIN invoices ref ON ref.id = iv.references_invoice_id
		  WHERE EXTRACT(YEAR FROM iv.issued_on) = $1
		    AND (iv.kind = 'invoice'
		         OR (iv.kind = 'storno' AND iv.status = 'issued' AND COALESCE(ref.kind,'') <> 'anzahlung')
		         OR (iv.kind NOT IN ('invoice','storno','anzahlung') AND iv.status = 'issued'))`, calYear).Scan(&sum)
	return sum, err
}
