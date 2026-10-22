package store

import (
	"context"
	"time"
)

// RetentionCandidate is an archived neighbor whose records have passed the
// retention period and could be anonymized.
type RetentionCandidate struct {
	NeighborID   int64
	Name         string
	LastActivity time.Time
	Invoices     int
}

// RetentionCutoff is the first day that still lies inside a retention period
// of the given full calendar years: records dated before it may go (§ 132 BAO
// counts from the end of the calendar year).
func RetentionCutoff(now time.Time, years int) time.Time {
	return time.Date(now.Year()-years, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// RetentionCandidates lists archived, not yet anonymized neighbors whose last
// booking, account posting, payment and invoice all lie before cutoff (a
// neighbor without any lies there by its creation date). Oldest first.
func (s *Store) RetentionCandidates(ctx context.Context, cutoff time.Time) ([]RetentionCandidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.name, a.last_activity,
		       (SELECT count(*) FROM invoices i WHERE i.neighbor_id = n.id)
		  FROM neighbors n
		  CROSS JOIN LATERAL (SELECT COALESCE(GREATEST(
		         (SELECT max(e.entry_date) FROM entries e WHERE e.neighbor_id = n.id),
		         (SELECT max(l.posting_date) FROM neighbor_ledger l WHERE l.neighbor_id = n.id),
		         (SELECT max(p.paid_on) FROM payments p WHERE p.neighbor_id = n.id),
		         (SELECT max(i.issued_on) FROM invoices i WHERE i.neighbor_id = n.id)),
		         n.created_at::date) AS last_activity) a
		 WHERE n.archived AND NOT n.anonymized AND a.last_activity < $1::date
		 ORDER BY a.last_activity, n.name`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RetentionCandidate
	for rows.Next() {
		var c RetentionCandidate
		if err := rows.Scan(&c.NeighborID, &c.Name, &c.LastActivity, &c.Invoices); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
