package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

// Recalc apply outcomes that the caller distinguishes from a generic failure.
var (
	// ErrYearCompleted: the year was closed before the write ran.
	ErrYearCompleted = errors.New("billing year is completed")
	// ErrRecalcConflict: a booking changed between preview and apply.
	ErrRecalcConflict = errors.New("booking changed since preview")
)

// RecalcRow is one booking's before/after when re-pricing it against the current
// values of its billing year's basis. Changed is true only when the money
// (rate or cost) actually differs — label reordering alone does not count.
type RecalcRow struct {
	EntryID       int64
	Date          time.Time
	NeighborID    int64
	NeighborName  string
	TaskLabel     string
	Hours         decimal.Decimal
	OldRate       decimal.Decimal
	OldCost       decimal.Decimal
	NewRate       decimal.Decimal
	NewCost       decimal.Decimal
	TractorLabel  string
	LoadLabel     string
	MachineLabels string
	Changed       bool
}

// RecalcPreview recomputes each non-voided booking of a year (optionally a single
// neighbor) from the current values of the year's basis items — same tractor/
// load/machines as booked, current prices — without writing. Bookings whose
// items no longer resolve (e.g. missing) are returned unchanged.
func (s *Store) RecalcPreview(ctx context.Context, yearID int64, neighborID *int64) ([]RecalcRow, error) {
	year, err := s.GetBillingYear(ctx, yearID)
	if err != nil {
		return nil, err
	}
	tractors, err := s.ListTractors(ctx, year.BaseID)
	if err != nil {
		return nil, err
	}
	loads, err := s.ListLoadLevels(ctx, year.BaseID)
	if err != nil {
		return nil, err
	}
	machines, err := s.ListMachines(ctx, year.BaseID)
	if err != nil {
		return nil, err
	}
	tByID := make(map[int64]models.Tractor, len(tractors))
	for _, t := range tractors {
		tByID[t.ID] = t
	}
	lByID := make(map[int64]models.LoadLevel, len(loads))
	for _, l := range loads {
		lByID[l.ID] = l
	}
	mByID := make(map[int64]models.Machine, len(machines))
	for _, m := range machines {
		mByID[m.ID] = m
	}

	var entries []models.Entry
	if neighborID != nil {
		entries, err = s.ListEntries(ctx, *neighborID, yearID)
	} else {
		entries, err = s.ListEntriesByYear(ctx, yearID)
	}
	if err != nil {
		return nil, err
	}
	emMap, err := s.entryMachineIDs(ctx, yearID)
	if err != nil {
		return nil, err
	}
	ns, err := s.ListNeighbors(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(ns))
	for _, n := range ns {
		names[n.ID] = n.Name
	}

	out := make([]RecalcRow, 0, len(entries))
	for _, e := range entries {
		if e.Voided {
			continue
		}
		row := RecalcRow{
			EntryID: e.ID, Date: e.Date, NeighborID: e.NeighborID, NeighborName: names[e.NeighborID],
			TaskLabel: e.TaskLabel, Hours: e.Hours,
			OldRate: e.HourlyRate, OldCost: e.Cost, NewRate: e.HourlyRate, NewCost: e.Cost,
			TractorLabel: e.TractorLabel, LoadLabel: e.LoadLabel, MachineLabels: e.MachineLabels,
		}
		if e.TractorID != nil && e.LoadLevelID != nil {
			t, tok := tByID[*e.TractorID]
			l, lok := lByID[*e.LoadLevelID]
			if tok && lok {
				var ms []models.Machine
				mnames := make([]string, 0)
				for _, mid := range emMap[e.ID] {
					if m, ok := mByID[mid]; ok {
						ms = append(ms, m)
						mnames = append(mnames, m.Name)
					}
				}
				rate := calc.GespannRate(t, l, ms)
				cost := calc.Cost(e.Hours, rate)
				row.NewRate, row.NewCost = rate, cost
				row.TractorLabel, row.LoadLabel = t.Label(), l.Name
				row.MachineLabels = strings.Join(mnames, ", ")
				row.Changed = !rate.Equal(e.HourlyRate) || !cost.Equal(e.Cost)
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// entryMachineIDs returns machine ids per entry for a year (deterministic order).
func (s *Store) entryMachineIDs(ctx context.Context, yearID int64) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT em.entry_id, em.machine_id
		  FROM entry_machines em
		  JOIN entries e ON e.id = em.entry_id
		 WHERE e.billing_year_id = $1 AND em.machine_id IS NOT NULL
		 ORDER BY em.entry_id, em.machine_id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var eid, mid int64
		if err := rows.Scan(&eid, &mid); err != nil {
			return nil, err
		}
		out[eid] = append(out[eid], mid)
	}
	return out, rows.Err()
}

// ApplyRecalc writes the recomputed rate/cost/labels for the changed bookings of
// a year (optionally one neighbor) in a single transaction, and returns how many
// were updated plus the old/new cost totals for the audit trail.
func (s *Store) ApplyRecalc(ctx context.Context, yearID int64, neighborID *int64) (updated int, oldTotal, newTotal decimal.Decimal, err error) {
	rows, err := s.RecalcPreview(ctx, yearID, neighborID)
	if err != nil {
		return 0, oldTotal, newTotal, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, oldTotal, newTotal, err
	}
	defer func() { _ = tx.Rollback() }()

	// Re-check the year status inside the transaction (locking the row) so a
	// concurrent "complete year" cannot be raced past the handler's pre-check.
	var status string
	if e := tx.QueryRowContext(ctx,
		`SELECT status FROM billing_years WHERE id=$1 FOR UPDATE`, yearID).Scan(&status); e != nil {
		return 0, oldTotal, newTotal, e
	}
	if status == models.YearCompleted {
		return 0, oldTotal, newTotal, ErrYearCompleted
	}

	for _, r := range rows {
		if !r.Changed {
			continue
		}
		// Optimistic guard: only overwrite the booking if it still holds the
		// values the preview was computed from. If it was edited concurrently
		// since then, abort rather than clobber the newer data.
		res, e := tx.ExecContext(ctx, `
			UPDATE entries SET hourly_rate=$1, cost=$2, tractor_label=$3, load_label=$4, machine_labels=$5
			 WHERE id=$6 AND hourly_rate=$7 AND cost=$8`,
			r.NewRate, r.NewCost, r.TractorLabel, r.LoadLabel, r.MachineLabels, r.EntryID, r.OldRate, r.OldCost)
		if e != nil {
			return 0, oldTotal, newTotal, e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return 0, oldTotal, newTotal, e
		}
		if n == 0 {
			return 0, oldTotal, newTotal, ErrRecalcConflict
		}
		updated++
		oldTotal = oldTotal.Add(r.OldCost)
		newTotal = newTotal.Add(r.NewCost)
	}
	if err = tx.Commit(); err != nil {
		return 0, oldTotal, newTotal, err
	}
	return updated, oldTotal, newTotal, nil
}
