package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
)

// DunningRow is one overdue neighbor in a billing year: an issued invoice whose
// remaining payable is still positive and whose due date (issue date + payment
// term) has passed.
type DunningRow struct {
	YearID      int64
	YearNo      int
	NeighborID  int64
	Name        string
	InvoiceNo   string
	IssuedOn    time.Time
	DueOn       time.Time
	TermDays    int // effective term: the neighbor's override or the company default
	DaysOverdue int
	Open        decimal.Decimal // remaining payable on the issued invoice (> 0)
}

// DunningRows returns the overdue rows for a billing year as of `asOf`, using the
// company payment term (days). Open is the amount STILL PAYABLE on the frozen
// invoice — its gross, less active credit notes and ledger and payments — the
// exact figure InvoiceRemaining/the invoice EPC-QR use, so the dunning list, the
// reminder and both QR codes always agree (a net/bookings figure would drop the
// VAT for pauschal/regel companies). Only issued (not canceled) invoices count —
// you can't dun an amount you never invoiced.
// yearID 0 means ALL years — the cross-year open-items list (Altersstaffel).
// The term is per row: a neighbor's payment_term_days override wins over the
// company default handed in.
// openAmountJoins is the shared core of the remaining-payable figure: the
// grouped credit/ledger/payment sums an issued invoice is reduced by. Joined
// once per child table instead of three correlated subqueries per invoice row
// — the cross-year Altersstaffel (yearID 0) materializes one row per invoice
// EVER issued, and the correlated form re-scanned each child table per row.
// YearClosingChecks consumes the same fragment; the single-pair form is
// InvoiceRemaining (company.go) — change all of them together.
const openAmountJoins = `
	  LEFT JOIN (SELECT billing_year_id, neighbor_id, SUM(gross) AS s FROM invoices
	              WHERE kind = 'gutschrift' AND status = 'issued'
	              GROUP BY billing_year_id, neighbor_id) cr
	         ON cr.billing_year_id = iv.billing_year_id AND cr.neighbor_id = iv.neighbor_id
	  LEFT JOIN (SELECT billing_year_id, neighbor_id, SUM(amount) AS s FROM neighbor_ledger
	              WHERE NOT voided
	              GROUP BY billing_year_id, neighbor_id) led
	         ON led.billing_year_id = iv.billing_year_id AND led.neighbor_id = iv.neighbor_id
	  LEFT JOIN (SELECT billing_year_id, neighbor_id, SUM(amount) AS s FROM payments
	              WHERE deleted_at IS NULL
	              GROUP BY billing_year_id, neighbor_id) pay
	         ON pay.billing_year_id = iv.billing_year_id AND pay.neighbor_id = iv.neighbor_id`

// openAmountExpr is the remaining payable built from those joins.
const openAmountExpr = `COALESCE(iv.gross, 0) + COALESCE(cr.s, 0) + COALESCE(led.s, 0) - COALESCE(pay.s, 0)`

func (s *Store) DunningRows(ctx context.Context, yearID int64, termDays int, asOf time.Time) ([]DunningRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH inv AS (
		  SELECT iv.billing_year_id, y.year AS year_no, iv.neighbor_id, n.name, iv.number, iv.issued_on,
		    COALESCE(n.payment_term_days, $2) AS term_days,
		    `+openAmountExpr+` AS open_amt
		  FROM invoices iv
		  JOIN neighbors n ON n.id = iv.neighbor_id
		  JOIN billing_years y ON y.id = iv.billing_year_id
		  `+openAmountJoins+`
		  WHERE ($1 = 0 OR iv.billing_year_id = $1) AND iv.kind = 'invoice' AND iv.status = 'issued'
		)
		SELECT billing_year_id, year_no, neighbor_id, name, number, issued_on, term_days, open_amt
		FROM inv
		WHERE open_amt > 0
		  AND $3::timestamptz > issued_on + make_interval(days => term_days)
		ORDER BY issued_on, name`, yearID, termDays, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DunningRow
	for rows.Next() {
		var r DunningRow
		if err := rows.Scan(&r.YearID, &r.YearNo, &r.NeighborID, &r.Name, &r.InvoiceNo, &r.IssuedOn, &r.TermDays, &r.Open); err != nil {
			return nil, err
		}
		r.DueOn = r.IssuedOn.AddDate(0, 0, r.TermDays)
		// Whole-day, DST-safe overdue count (shared with the Beleg due-date line via
		// calc.DaysBetween), so the dunning list, CSV export and Beleg all agree.
		if d := calc.DaysBetween(r.DueOn, asOf); d > 0 {
			r.DaysOverdue = d
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// NeighborNetPaid returns the net owed and the paid total for one neighbor in a
// billing year, using the same definition as the dashboard/dunning list.
func (s *Store) NeighborNetPaid(ctx context.Context, yearID, neighborID int64) (net, paid decimal.Decimal, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE((SELECT SUM(e.cost) FROM entries e
		             WHERE e.neighbor_id=$2 AND e.billing_year_id=$1 AND NOT e.voided), 0)
		  + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		               WHERE l.neighbor_id=$2 AND l.billing_year_id=$1 AND NOT l.voided), 0),
		  COALESCE((SELECT SUM(p.amount) FROM payments p
		             WHERE p.neighbor_id=$2 AND p.billing_year_id=$1 AND p.deleted_at IS NULL), 0)`,
		yearID, neighborID).Scan(&net, &paid)
	return
}
