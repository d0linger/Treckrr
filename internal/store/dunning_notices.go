package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// DunningNotice is one recorded reminder/Mahnung for a neighbor+year.
type DunningNotice struct {
	ID            int64
	BillingYearID int64
	NeighborID    int64
	InvoiceNumber string
	Stage         int
	Channel       string
	SentAt        time.Time
	GraceUntil    time.Time
	Fee           decimal.Decimal
}

// RecordDunningNotice writes one notice into the dunning history. graceUntil may
// be zero (no Nachfrist printed).
func (s *Store) RecordDunningNotice(ctx context.Context, n DunningNotice) error {
	var grace any
	if !n.GraceUntil.IsZero() {
		grace = n.GraceUntil
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO dunning_notices (billing_year_id, neighbor_id, invoice_number, stage, channel, grace_until, fee)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		n.BillingYearID, n.NeighborID, n.InvoiceNumber, n.Stage, n.Channel, grace, n.Fee)
	return err
}

// LastDunningNotices returns the most recent notice per neighbor for one billing
// year — what the dunning list shows as "zuletzt: 1. Mahnung am 05.09.".
// The map holds POINTERS so the template's {{with (index . id)}} stays false for
// a neighbor without any notice — a zero struct value would be truthy and render
// an empty "Zuletzt:" line for everyone.
func (s *Store) LastDunningNotices(ctx context.Context, yearID int64) (map[int64]*DunningNotice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT ON (neighbor_id) id, billing_year_id, neighbor_id, invoice_number,
		        stage, channel, sent_at, COALESCE(grace_until, '0001-01-01'::date), fee
		   FROM dunning_notices
		  WHERE billing_year_id = $1
		  ORDER BY neighbor_id, sent_at DESC`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*DunningNotice{}
	for rows.Next() {
		n := new(DunningNotice)
		if err := rows.Scan(&n.ID, &n.BillingYearID, &n.NeighborID, &n.InvoiceNumber,
			&n.Stage, &n.Channel, &n.SentAt, &n.GraceUntil, &n.Fee); err != nil {
			return nil, err
		}
		out[n.NeighborID] = n
	}
	return out, rows.Err()
}
