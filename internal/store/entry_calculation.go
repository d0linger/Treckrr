package store

import (
	"context"

	"github.com/shopspring/decimal"
)

// EntryMachineSnapshot is one immutable machine component used to explain a
// booked hourly rate. Values come from the booking snapshot, not the catalog.
type EntryMachineSnapshot struct {
	EntryID    int64
	Label      string
	HourlyRate decimal.Decimal
}

// EntryMachineSnapshotsByNeighborYear returns immutable component prices for
// every booking in one account. Pre-snapshot bookings simply have no rows.
func (s *Store) EntryMachineSnapshotsByNeighborYear(ctx context.Context, neighborID, yearID int64) (map[int64][]EntryMachineSnapshot, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT snap.entry_id, snap.machine_label, snap.hourly_rate
		  FROM entry_machine_snapshots snap
		  JOIN entries e ON e.id=snap.entry_id
		 WHERE e.neighbor_id=$1 AND e.billing_year_id=$2
		 ORDER BY snap.entry_id, snap.id`, neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64][]EntryMachineSnapshot)
	for rows.Next() {
		var item EntryMachineSnapshot
		if err := rows.Scan(&item.EntryID, &item.Label, &item.HourlyRate); err != nil {
			return nil, err
		}
		out[item.EntryID] = append(out[item.EntryID], item)
	}
	return out, rows.Err()
}
