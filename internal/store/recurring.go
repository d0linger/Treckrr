package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// CreateRecurring stores a new recurring-booking rule.
func (s *Store) CreateRecurring(ctx context.Context, neighborID int64, t models.RecurTemplate, intervalKind string, nextRun time.Time) error {
	blob, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO recurring_entries (neighbor_id, template, interval_kind, next_run)
		 VALUES ($1,$2,$3,$4)`, neighborID, blob, intervalKind, nextRun)
	return err
}

// ListRecurring returns all rules with their neighbor name, newest first.
func (s *Store) ListRecurring(ctx context.Context) ([]models.RecurringEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT re.id, re.neighbor_id, n.name, re.template, re.interval_kind,
		        re.next_run, re.active, re.created_at, re.last_run_at
		   FROM recurring_entries re JOIN neighbors n ON n.id = re.neighbor_id
		  ORDER BY re.active DESC, re.next_run`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []models.RecurringEntry
	for rows.Next() {
		var r models.RecurringEntry
		var blob []byte
		var last sql.NullTime
		if err := rows.Scan(&r.ID, &r.NeighborID, &r.NeighborName, &blob, &r.IntervalKind,
			&r.NextRun, &r.Active, &r.CreatedAt, &last); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(blob, &r.Template); err != nil {
			return nil, err
		}
		if last.Valid {
			r.LastRunAt = &last.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ToggleRecurring flips a rule's active flag.
func (s *Store) ToggleRecurring(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recurring_entries SET active = NOT active WHERE id=$1`, id)
	return err
}

// DeleteRecurring removes a rule (already-created bookings are untouched).
func (s *Store) DeleteRecurring(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM recurring_entries WHERE id=$1`, id)
	return err
}

// advanceDate steps a date by one cadence.
func advanceDate(d time.Time, kind string) time.Time {
	if kind == "monthly" {
		return d.AddDate(0, 1, 0)
	}
	return d.AddDate(0, 0, 7) // weekly (default)
}

// neighborYearForDate returns the non-completed billing year matching the date's
// calendar year that the neighbor participates in — the year a generated booking
// belongs to. ok=false if there's no such open year (booking is then skipped).
func (s *Store) neighborYearForDate(ctx context.Context, neighborID int64, d time.Time) (int64, bool, error) {
	var yid int64
	err := s.db.QueryRowContext(ctx,
		`SELECT by.id FROM billing_years by
		   JOIN billing_year_neighbors byn ON byn.billing_year_id = by.id
		  WHERE byn.neighbor_id=$1 AND by.year=$2 AND by.status <> 'completed'
		  LIMIT 1`, neighborID, d.Year()).Scan(&yid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return yid, true, nil
}

// RunDueRecurring materializes every due occurrence of every active rule as a
// normal booking and advances the rule. It is idempotent: each occurrence carries
// idempotency_key "recur:<rule>:<date>", so a restart or overlapping tick never
// double-books. A per-rule cap bounds catch-up after downtime. Returns the number
// of bookings actually created.
func (s *Store) RunDueRecurring(ctx context.Context) (int, error) {
	today := time.Now().Truncate(24 * time.Hour)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, neighbor_id, template, interval_kind, next_run
		   FROM recurring_entries WHERE active AND next_run <= $1`, today)
	if err != nil {
		return 0, err
	}
	type due struct {
		id, neighborID int64
		tmpl           models.RecurTemplate
		kind           string
		next           time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		var blob []byte
		if err := rows.Scan(&d.id, &d.neighborID, &blob, &d.kind, &d.next); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if err := json.Unmarshal(blob, &d.tmpl); err != nil {
			_ = rows.Close()
			return 0, err
		}
		list = append(list, d)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	created := 0
	for _, d := range list {
		next := d.next
		var lastRun time.Time
		for i := 0; i < 60 && !next.After(today); i++ { // cap catch-up per rule per tick
			yid, ok, yerr := s.neighborYearForDate(ctx, d.neighborID, next)
			if yerr != nil {
				return created, yerr
			}
			if ok {
				e := entryFromTemplate(d.tmpl)
				e.NeighborID = d.neighborID
				e.BillingYearID = yid
				e.Date = next
				e.IdempotencyKey = fmt.Sprintf("recur:%d:%s", d.id, next.Format("2006-01-02"))
				id, cerr := s.CreateEntry(ctx, e, d.tmpl.MachineIDs)
				if cerr != nil {
					return created, cerr
				}
				if id != 0 {
					created++
				}
			} else {
				slog.Warn("recurring booking skipped: no open year", "rule", d.id, "neighbor", d.neighborID, "date", next.Format("2006-01-02"))
			}
			lastRun = next
			next = advanceDate(next, d.kind)
		}
		if _, err := s.db.ExecContext(ctx,
			`UPDATE recurring_entries SET next_run=$1, last_run_at=$2 WHERE id=$3`,
			next, lastRun, d.id); err != nil {
			return created, err
		}
	}
	return created, nil
}

// entryFromTemplate rebuilds an Entry from a recurring template (cost recomputed by
// CreateEntry's callers is not needed — the template already carries Cost).
func entryFromTemplate(t models.RecurTemplate) *models.Entry {
	return &models.Entry{
		TaskLabel:     t.TaskLabel,
		Note:          t.Note,
		Unit:          t.Unit,
		Quantity:      t.Quantity,
		UnitPrice:     t.UnitPrice,
		Hours:         t.Hours,
		HourlyRate:    t.HourlyRate,
		Cost:          t.Cost,
		TractorID:     t.TractorID,
		LoadLevelID:   t.LoadLevelID,
		TractorLabel:  t.TractorLabel,
		LoadLabel:     t.LoadLabel,
		MachineLabels: t.MachineLabels,
	}
}
