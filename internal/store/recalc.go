package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
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
	// Festschreibung: a neighbor with an issued invoice has frozen bookings — never
	// re-price them (BAO §131). Drop their entries here so both the preview and the
	// apply (which iterates the preview rows) leave the invoiced basis untouched,
	// whether the scope is one neighbor or the whole year.
	locked, err := s.InvoicedNeighborIDs(ctx, yearID)
	if err != nil {
		return nil, err
	}
	if len(locked) > 0 {
		kept := entries[:0]
		for _, e := range entries {
			if !locked[e.NeighborID] {
				kept = append(kept, e)
			}
		}
		entries = kept
	}
	emMap, err := s.entryMachineIDs(ctx, yearID, neighborID)
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

// entryMachineIDs returns machine ids per entry (deterministic order), for a
// whole year or for a single neighbor within it.
//
// The neighbor filter matters: RecalcPreview is called per-neighbor on every
// neighbor-detail render, and without it this loaded the machine map for the
// ENTIRE year to price one neighbor's bookings.
func (s *Store) entryMachineIDs(ctx context.Context, yearID int64, neighborID *int64) (map[int64][]int64, error) {
	query := `
		SELECT em.entry_id, em.machine_id
		  FROM entry_machines em
		  JOIN entries e ON e.id = em.entry_id
		 WHERE e.billing_year_id = $1 AND em.machine_id IS NOT NULL`
	args := []any{yearID}
	if neighborID != nil {
		query += ` AND e.neighbor_id = $2`
		args = append(args, *neighborID)
	}
	query += ` ORDER BY em.entry_id, em.machine_id`
	rows, err := s.db.QueryContext(ctx, query, args...)
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
	// Basis revision as it stood when the preview was priced. If a price item is
	// edited between the preview and the writes below, the new rates would come
	// from a basis that no longer exists AND priced_at would be advanced past
	// items_updated_at — marking the bookings fresh against values never applied.
	revBefore, err := s.basisRevision(ctx, yearID)
	if err != nil {
		return 0, oldTotal, newTotal, err
	}
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
	// Same optimistic treatment the per-booking guard below uses, one level up.
	revNow, e := s.basisRevisionTx(ctx, tx, yearID)
	if e != nil {
		return 0, oldTotal, newTotal, e
	}
	if !revNow.Equal(revBefore) {
		return 0, oldTotal, newTotal, ErrRecalcConflict
	}

	for _, r := range rows {
		if !r.Changed {
			// Stamp it anyway. The money did not move, but the booking WAS
			// revalidated against the current basis, so it is not stale. Skipping
			// this left every unchanged row with an old priced_at, so the 0040 gate
			// stayed permanently positive after the first partial recalc and the
			// full preview ran on every render again — the optimisation dying
			// silently. Guarded like the write below; a miss just means the row was
			// edited concurrently, and the next run picks it up, so it is not an error.
			if _, e := tx.ExecContext(ctx,
				`UPDATE entries SET priced_at = now()
				  WHERE id=$1 AND hourly_rate=$2 AND cost=$3`,
				r.EntryID, r.OldRate, r.OldCost); e != nil {
				return 0, oldTotal, newTotal, e
			}
			continue
		}
		// Optimistic guard: only overwrite the booking if it still holds the
		// values the preview was computed from. If it was edited concurrently
		// since then, abort rather than clobber the newer data.
		res, e := tx.ExecContext(ctx, `
			UPDATE entries SET hourly_rate=$1, cost=$2, tractor_label=$3, load_label=$4, machine_labels=$5,
			       unit_price = CASE WHEN unit='h' THEN $1 ELSE unit_price END,
			       priced_at = now()
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

// CountPotentiallyStale reports how many non-voided bookings were priced BEFORE
// the last change to their year's basis — an exact-negative gate for the
// repricing badge (0040).
//
// Zero means nothing can be stale, so the caller may skip RecalcPreview entirely.
// A non-zero result is an upper bound, not the answer: a basis edit to an unused
// tractor marks bookings that would reprice to the same amount. Callers must run
// the full preview to get the number they display — this only decides whether
// that work is worth doing.
// basisRevision returns the year's price-basis revision stamp (0040).
func (s *Store) basisRevision(ctx context.Context, yearID int64) (time.Time, error) {
	var ts time.Time
	err := s.db.QueryRowContext(ctx, basisRevisionSQL, yearID).Scan(&ts)
	return ts, err
}

// basisRevisionTx is basisRevision read through an open transaction.
func (s *Store) basisRevisionTx(ctx context.Context, tx *sql.Tx, yearID int64) (time.Time, error) {
	var ts time.Time
	err := tx.QueryRowContext(ctx, basisRevisionSQL, yearID).Scan(&ts)
	return ts, err
}

const basisRevisionSQL = `SELECT b.items_updated_at
	  FROM billing_years y JOIN price_bases b ON b.id = y.base_id
	 WHERE y.id = $1`

// notInvoiced keeps the gate's scope identical to RecalcPreview's, which skips
// neighbors whose invoice is already issued — their bookings are frozen and are
// never re-priced. Counting them meant the gate could never reach zero for such a
// year, so the expensive preview ran on every render and then found nothing to do.
const notInvoiced = `
	   AND NOT EXISTS (SELECT 1 FROM invoices iv
	                    WHERE iv.billing_year_id = e.billing_year_id
	                      AND iv.neighbor_id     = e.neighbor_id
	                      AND iv.kind = 'invoice' AND iv.status = 'issued')`

func (s *Store) CountPotentiallyStale(ctx context.Context, yearID int64, neighborID *int64) (int, error) {
	var n int
	var err error
	if neighborID != nil {
		err = s.db.QueryRowContext(ctx, `
			SELECT count(*)
			  FROM entries e
			  JOIN billing_years y ON y.id = e.billing_year_id
			  JOIN price_bases  b ON b.id = y.base_id
			 WHERE e.billing_year_id = $1 AND e.neighbor_id = $2
			   AND NOT e.voided AND e.priced_at < b.items_updated_at`+notInvoiced,
			yearID, *neighborID).Scan(&n)
	} else {
		err = s.db.QueryRowContext(ctx, `
			SELECT count(*)
			  FROM entries e
			  JOIN billing_years y ON y.id = e.billing_year_id
			  JOIN price_bases  b ON b.id = y.base_id
			 WHERE e.billing_year_id = $1
			   AND NOT e.voided AND e.priced_at < b.items_updated_at`+notInvoiced,
			yearID).Scan(&n)
	}
	return n, err
}
