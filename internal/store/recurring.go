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
// ErrSourceEntryVoided reports that the booking a series was to be created from
// was canceled between the handler's check and the insert.
var ErrSourceEntryVoided = errors.New("source entry is voided")

// ErrSourceCompanionUnavailable reports that the selected helper booking was
// canceled, removed, or changed before the recurring template could be saved.
var ErrSourceCompanionUnavailable = errors.New("source companion is unavailable")

func (s *Store) CreateRecurring(ctx context.Context, sourceEntryID, neighborID int64, t models.RecurTemplate, intervalKind string, nextRun time.Time) error {
	blob, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var companionPersonID int64
	if t.Companion != nil {
		companionPersonID = t.Companion.PersonID
	}
	// Recheck and lock the source and selected companion through insertion.
	// Ascending IDs match DeleteEntryPair, avoiding a source/companion deadlock.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, voided FROM entries
		 WHERE id=$1 OR (linked_entry_id=$1 AND person_id=$2 AND unit_price > 0)
		 ORDER BY id FOR SHARE`, sourceEntryID, companionPersonID)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var sourceFound, sourceVoided, companionAvailable bool
	for rows.Next() {
		var id int64
		var voided bool
		if err := rows.Scan(&id, &voided); err != nil {
			return err
		}
		if id == sourceEntryID {
			sourceFound, sourceVoided = true, voided
		} else if !voided {
			companionAvailable = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if !sourceFound {
		return ErrNotFound
	}
	if sourceVoided {
		return ErrSourceEntryVoided
	}
	if t.Companion != nil && !companionAvailable {
		return ErrSourceCompanionUnavailable
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO recurring_entries (neighbor_id, template, interval_kind, next_run)
		 VALUES ($1,$2,$3,$4)`, neighborID, blob, intervalKind, nextRun); err != nil {
		return err
	}
	return tx.Commit()
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

// ToggleRecurring flips a rule's active flag and returns the new state, so the
// caller can audit "paused" vs "resumed". ErrNotFound when the id doesn't exist.
func (s *Store) ToggleRecurring(ctx context.Context, id int64) (active bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`UPDATE recurring_entries SET active = NOT active WHERE id=$1 RETURNING active`, id).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return active, err
}

// DeleteRecurring removes a rule (already-created bookings are untouched) and
// returns the neighbor it belonged to, so the caller can name it in the audit
// trail. ErrNotFound when the id doesn't exist.
func (s *Store) DeleteRecurring(ctx context.Context, id int64) (neighborID int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`DELETE FROM recurring_entries WHERE id=$1 RETURNING neighbor_id`, id).Scan(&neighborID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return neighborID, err
}

// advanceDate steps a date by one cadence. Monthly clamps to the target month's
// last day (so a rule on the 31st doesn't skip February and land on March 3rd via
// AddDate's overflow); it still generates an occurrence every month.
func advanceDate(d time.Time, kind string) time.Time {
	if kind == "monthly" {
		y, m, day := d.Date()
		firstNext := time.Date(y, m, 1, 0, 0, 0, 0, d.Location()).AddDate(0, 1, 0)
		if last := firstNext.AddDate(0, 1, -1).Day(); day > last {
			day = last
		}
		return time.Date(firstNext.Year(), firstNext.Month(), day, 0, 0, 0, 0, d.Location())
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
// of occurrences with a new booking, including a restored companion alone.
func (s *Store) RunDueRecurring(ctx context.Context) (int, error) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()) // local midnight, not UTC
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
		var lastRun *time.Time
		// Resolved once per rule, not per occurrence: a catch-up run materializes
		// up to 60 of them and the answer is the same for all.
		comp, personID, lerr := s.liveRefs(ctx, d.tmpl, d.id)
		if lerr != nil {
			return created, lerr
		}
		for i := 0; i < 60 && !next.After(today); i++ { // cap catch-up per rule per tick
			yid, ok, yerr := s.neighborYearForDate(ctx, d.neighborID, next)
			if yerr != nil {
				return created, yerr
			}
			if !ok {
				// No open year for this date yet. Stop WITHOUT advancing so the
				// occurrence is retried once that year opens, instead of being
				// skipped past forever (which would silently drop the booking).
				slog.Warn("recurring booking waiting: no open year", "rule", d.id, "neighbor", d.neighborID, "date", next.Format("2006-01-02"))
				break
			}
			e := entryFromTemplate(d.tmpl)
			e.PersonID = personID // nil once the helper record is gone
			e.NeighborID = d.neighborID
			e.BillingYearID = yid
			e.Date = next
			e.IdempotencyKey = fmt.Sprintf("recur:%d:%s", d.id, next.Format("2006-01-02"))
			var id, companionID int64
			var cerr error
			if companion := companionEntry(comp, e); companion != nil {
				// Counting stays per OCCURRENCE, not per row: the companion is the
				// same piece of work as the machine booking it is linked to.
				id, companionID, cerr = s.CreateEntryPair(ctx, e, d.tmpl.MachineIDs, companion)
			} else {
				id, cerr = s.CreateEntry(ctx, e, d.tmpl.MachineIDs)
			}
			if cerr != nil {
				return created, cerr
			}
			if id != 0 || companionID != 0 {
				created++
			}
			ran := next
			lastRun = &ran
			next = advanceDate(next, d.kind)
		}
		// Always persist next_run (unchanged if we're waiting); touch last_run_at
		// only when an occurrence actually ran.
		if lastRun != nil {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE recurring_entries SET next_run=$1, last_run_at=$2 WHERE id=$3`, next, *lastRun, d.id); err != nil {
				return created, err
			}
		} else if !next.Equal(d.next) {
			if _, err := s.db.ExecContext(ctx,
				`UPDATE recurring_entries SET next_run=$1 WHERE id=$2`, next, d.id); err != nil {
				return created, err
			}
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
		GespannID:     t.GespannID,
		TractorID:     t.TractorID,
		LoadLevelID:   t.LoadLevelID,
		TractorLabel:  t.TractorLabel,
		LoadLabel:     t.LoadLabel,
		MachineLabels: t.MachineLabels,
		// A series made FROM a Mannstunden booking keeps its attribution: the
		// template carried the person id but the rebuilt entry dropped it, so
		// every occurrence booked the helper's hours as nobody's.
		PersonID: t.PersonID,
	}
}

// companionEntry builds the helper's Mannstunden booking that accompanies a
// generated machine booking — the frozen counterpart of the person selected on
// the booking form. nil when the series carries no helper, when the frozen rate
// cannot price anything, or when the occurrence is not an hour booking: a
// quantity booking (ha, Ballen, …) carries no hours the helper's time could be
// derived from, the same rule handleEntryCreate applies.
func companionEntry(c *models.RecurCompanion, e *models.Entry) *models.Entry {
	if c == nil || !e.Hours.IsPositive() || !c.Rate.IsPositive() || (e.Unit != "" && e.Unit != "h") {
		return nil
	}
	personID := c.PersonID
	return &models.Entry{
		NeighborID: e.NeighborID, BillingYearID: e.BillingYearID, Date: e.Date,
		TaskLabel: "Mannstunden " + c.Name,
		Unit:      models.UnitMannstunde,
		Quantity:  e.Hours, UnitPrice: c.Rate,
		Cost:     e.Hours.Mul(c.Rate).Round(2),
		PersonID: &personID,
		// Derived from the occurrence's own key, so a re-run no-ops on both
		// halves exactly as it does for a replayed offline pair.
		IdempotencyKey: models.CompanionKey(e.IdempotencyKey),
	}
}

// liveRefs resolves BOTH helper references a template can carry — the
// companion's person and the booking's own attribution — against the
// Personenstamm, and drops whichever no longer exists.
//
// It has to: a helper can be deleted once no booking references them any more,
// and a rule outlives the booking it was made from (recurring_entries has no FK
// to entries). entries.person_id is a real foreign key, so inserting a stale id
// fails the occurrence AND, since RunDueRecurring returns on that error, every
// rule behind it — every tick, until someone notices. The booking is worth more
// than the attribution, so the occurrence is booked without it and the loss is
// logged.
//
// Resolved once per rule, before the occurrence loop. A helper deleted inside
// the remaining window still fails that one occurrence; the next tick sees the
// record gone and books it, so the failure heals itself rather than sticking.
func (s *Store) liveRefs(ctx context.Context, t models.RecurTemplate, ruleID int64) (*models.RecurCompanion, *int64, error) {
	ids := make([]int64, 0, 2)
	if t.Companion != nil {
		ids = append(ids, t.Companion.PersonID)
	}
	if t.PersonID != nil {
		ids = append(ids, *t.PersonID)
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM persons WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	live := make(map[int64]bool, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		live[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	comp, person := t.Companion, t.PersonID
	if comp != nil && !live[comp.PersonID] {
		slog.Warn("recurring companion skipped: helper record is gone",
			"rule", ruleID, "person", comp.PersonID)
		comp = nil
	}
	if person != nil && !live[*person] {
		slog.Warn("recurring booking loses its helper attribution: record is gone",
			"rule", ruleID, "person", *person)
		person = nil
	}
	return comp, person, nil
}

// ---- Serie bearbeiten und sofort ausführen (Ausbaukarte 68) ----------------

// UpdateRecurring changes a rule's cadence and next run date. The template
// stays as it was: it is a frozen copy of the source booking, and editing it
// here would mean rebuilding a booking form for a rule — the operator instead
// deletes the rule and sets up a new one from the corrected booking.
func (s *Store) UpdateRecurring(ctx context.Context, id int64, intervalKind string, nextRun time.Time) error {
	if intervalKind != "weekly" && intervalKind != "monthly" {
		intervalKind = "weekly"
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE recurring_entries SET interval_kind=$2, next_run=$3 WHERE id=$1`,
		id, intervalKind, nextRun)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RunRecurringNow materializes ONE extra occurrence of a rule, dated today,
// without touching the rhythm — "jetzt zusätzlich buchen", not "vorziehen".
// It reuses the scheduled run's idempotency key ("recur:<rule>:<date>"), so
// clicking twice on the same day books once, and today's scheduled run later
// finds the occurrence already there.
// The returned ID identifies a newly created entry, including a companion
// restored on its own; zero means neither half was created.
//
// Returns (0, false, nil) when the neighbor has no open billing year for
// today — the same condition the scheduled run waits on, reported to the
// operator instead of silently doing nothing.
func (s *Store) RunRecurringNow(ctx context.Context, id int64) (int64, bool, error) {
	var neighborID int64
	var blob []byte
	var active bool
	err := s.db.QueryRowContext(ctx,
		`SELECT neighbor_id, template, active FROM recurring_entries WHERE id=$1`, id).
		Scan(&neighborID, &blob, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrNotFound
	}
	if err != nil {
		return 0, false, err
	}
	if !active {
		return 0, false, ErrInactiveRule
	}
	var tmpl models.RecurTemplate
	if err := json.Unmarshal(blob, &tmpl); err != nil {
		return 0, false, err
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yid, ok, err := s.neighborYearForDate(ctx, neighborID, today)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	e := entryFromTemplate(tmpl)
	e.NeighborID = neighborID
	e.BillingYearID = yid
	e.Date = today
	e.IdempotencyKey = fmt.Sprintf("recur:%d:%s", id, today.Format("2006-01-02"))
	comp, personID, err := s.liveRefs(ctx, tmpl, id)
	if err != nil {
		return 0, false, err
	}
	e.PersonID = personID // nil once the helper record is gone
	var entryID int64
	if companion := companionEntry(comp, e); companion != nil {
		var companionID int64
		entryID, companionID, err = s.CreateEntryPair(ctx, e, tmpl.MachineIDs, companion)
		if entryID == 0 {
			entryID = companionID
		}
	} else {
		entryID, err = s.CreateEntry(ctx, e, tmpl.MachineIDs)
	}
	if err != nil {
		return 0, false, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE recurring_entries SET last_run_at=$2 WHERE id=$1`, id, today); err != nil {
		return entryID, true, err
	}
	return entryID, true, nil
}
