package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// DunningRow is one overdue neighbor in a billing year: an issued invoice whose
// open amount (owed − paid) is still positive and whose due date (issue date +
// payment term) has passed.
type DunningRow struct {
	NeighborID  int64
	Name        string
	InvoiceNo   string
	IssuedOn    time.Time
	DueOn       time.Time
	DaysOverdue int
	Owed        decimal.Decimal // net owed (bookings + signed ledger)
	Paid        decimal.Decimal
	Open        decimal.Decimal // Owed − Paid (> 0)
}

// DunningRows returns the overdue rows for a billing year as of `asOf`, using the
// company payment term (days). The owed/paid figures reuse the same net/paid
// definition as the dashboard so amounts always agree. Only neighbors with an
// issued (not canceled) invoice document are considered — you can't dun an
// amount you never invoiced.
func (s *Store) DunningRows(ctx context.Context, yearID int64, termDays int, asOf time.Time) ([]DunningRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH per AS (
		  SELECT n.id AS neighbor_id, n.name,
		    COALESCE((SELECT SUM(e.cost) FROM entries e
		               WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id
		                 AND NOT e.voided), 0)
		    + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		                 WHERE l.neighbor_id = n.id AND l.billing_year_id = byn.billing_year_id
		                   AND NOT l.voided), 0) AS owed,
		    COALESCE((SELECT SUM(p.amount) FROM payments p
		               WHERE p.neighbor_id = n.id AND p.billing_year_id = byn.billing_year_id), 0) AS paid
		  FROM billing_year_neighbors byn
		  JOIN neighbors n ON n.id = byn.neighbor_id
		  WHERE byn.billing_year_id = $1
		)
		SELECT per.neighbor_id, per.name, iv.number, iv.issued_on, per.owed, per.paid
		FROM per
		JOIN invoices iv ON iv.billing_year_id = $1 AND iv.neighbor_id = per.neighbor_id
		     AND iv.kind = 'invoice' AND iv.status = 'issued'
		WHERE (per.owed - per.paid) > 0
		  AND $3::timestamptz > iv.issued_on + make_interval(days => $2)
		ORDER BY iv.issued_on, per.name`, yearID, termDays, asOf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DunningRow
	for rows.Next() {
		var r DunningRow
		if err := rows.Scan(&r.NeighborID, &r.Name, &r.InvoiceNo, &r.IssuedOn, &r.Owed, &r.Paid); err != nil {
			return nil, err
		}
		r.Open = r.Owed.Sub(r.Paid)
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
