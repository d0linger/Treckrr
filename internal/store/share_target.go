package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// PhotoTarget is a recent booking that can still take receipt photos.
type PhotoTarget struct {
	EntryID  int64
	Date     time.Time
	Neighbor string
	Task     string
	Cost     decimal.Decimal
}

// RecentPhotoTargets lists the newest own bookings that may still receive
// photos: not voided, in a year that is not completed and not claimed by an
// active invoice (the same freeze the photo upload enforces).
func (s *Store) RecentPhotoTargets(ctx context.Context, limit int) ([]PhotoTarget, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, e.entry_date, n.name, e.task_label, e.cost
		  FROM entries e
		  JOIN neighbors n ON n.id = e.neighbor_id
		  JOIN billing_years y ON y.id = e.billing_year_id
		 WHERE NOT e.voided AND y.status <> 'completed' AND NOT n.anonymized
		   AND NOT EXISTS (SELECT 1 FROM invoice_entries ie WHERE ie.entry_id = e.id AND ie.active)
		 ORDER BY e.entry_date DESC, e.id DESC
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PhotoTarget{}
	for rows.Next() {
		var t PhotoTarget
		if err := rows.Scan(&t.EntryID, &t.Date, &t.Neighbor, &t.Task, &t.Cost); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
