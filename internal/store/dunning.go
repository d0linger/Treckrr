package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// DunningRow is one overdue neighbor in a billing year: an issued invoice whose
// remaining payable is still positive and whose due date (issue date + payment
// term) has passed.
type DunningRow struct {
	NeighborID  int64
	Name        string
	InvoiceNo   string
	IssuedOn    time.Time
	DueOn       time.Time
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
func (s *Store) DunningRows(ctx context.Context, yearID int64, termDays int, asOf time.Time) ([]DunningRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH inv AS (
		  SELECT iv.neighbor_id, n.name, iv.number, iv.issued_on,
		    COALESCE(iv.gross, 0)
		    + COALESCE((SELECT SUM(g.gross) FROM invoices g
		                 WHERE g.billing_year_id = $1 AND g.neighbor_id = iv.neighbor_id
		                   AND g.kind = 'gutschrift' AND g.status = 'issued'), 0)
		    + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		                 WHERE l.billing_year_id = $1 AND l.neighbor_id = iv.neighbor_id
		                   AND NOT l.voided), 0)
		    - COALESCE((SELECT SUM(p.amount) FROM payments p
		                 WHERE p.billing_year_id = $1 AND p.neighbor_id = iv.neighbor_id), 0) AS open_amt
		  FROM invoices iv
		  JOIN neighbors n ON n.id = iv.neighbor_id
		  WHERE iv.billing_year_id = $1 AND iv.kind = 'invoice' AND iv.status = 'issued'
		)
		SELECT neighbor_id, name, number, issued_on, open_amt
		FROM inv
		WHERE open_amt > 0
		  AND $3::timestamptz > issued_on + make_interval(days => $2)
		ORDER BY issued_on, name`, yearID, termDays, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DunningRow
	for rows.Next() {
		var r DunningRow
		if err := rows.Scan(&r.NeighborID, &r.Name, &r.InvoiceNo, &r.IssuedOn, &r.Open); err != nil {
			return nil, err
		}
		r.DueOn = r.IssuedOn.AddDate(0, 0, termDays)
		if d := int(asOf.Sub(r.DueOn).Hours() / 24); d > 0 {
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
		             WHERE p.neighbor_id=$2 AND p.billing_year_id=$1), 0)`,
		yearID, neighborID).Scan(&net, &paid)
	return
}
