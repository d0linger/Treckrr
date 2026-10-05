package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/money"
)

// ErrSourceEntryVoided reports that the booking a series was to be created from
// was canceled between the handler's check and the insert.
var ErrSourceEntryVoided = errors.New("source entry is voided")

// ErrSourceCompanionUnavailable reports that the selected helper booking was
// canceled, removed, or changed before the recurring template could be saved.
var ErrSourceCompanionUnavailable = errors.New("source companion is unavailable")

// ErrRecurringEndBeforeStart rejects a schedule whose optional end precedes its
// next occurrence.
var ErrRecurringEndBeforeStart = errors.New("recurring end date precedes next run")

// ErrRecurringEnded prevents reactivating or running an already ended series.
var ErrRecurringEnded = errors.New("recurring series has ended")

// CreateRecurring stores an unbounded recurring-booking rule for compatibility
// with callers that do not expose schedule bounds.
func (s *Store) CreateRecurring(ctx context.Context, sourceEntryID, neighborID int64, t models.RecurTemplate, intervalKind string, nextRun time.Time) error {
	return s.CreateRecurringUntil(ctx, sourceEntryID, neighborID, t, intervalKind, nextRun, nil)
}

// CreateRecurringUntil stores a new recurring rule with an optional inclusive
// end date. The compatibility wrapper above keeps existing callers unbounded.
func (s *Store) CreateRecurringUntil(ctx context.Context, sourceEntryID, neighborID int64, t models.RecurTemplate, intervalKind string, nextRun time.Time, endsOn *time.Time) error {
	if endsOn != nil && endsOn.Before(nextRun) {
		return ErrRecurringEndBeforeStart
	}
	blob, err := json.Marshal(t)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var yearID, sourceNeighbor int64
	if err := tx.QueryRowContext(ctx,
		`SELECT billing_year_id,neighbor_id FROM entries WHERE id=$1`, sourceEntryID).
		Scan(&yearID, &sourceNeighbor); err != nil {
		return err
	}
	if sourceNeighbor != neighborID {
		return ErrNotFound
	}
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	if t.Companions != nil {
		if err := validateRecurringGroup(ctx, tx, sourceEntryID, t.Companions); err != nil {
			return err
		}
	}
	var companionPersonID int64
	companionHours, companionRate := "0", "0"
	if t.Companion != nil {
		companionPersonID = t.Companion.PersonID
		companionHours, companionRate = t.Companion.Hours.String(), t.Companion.Rate.String()
	}
	// Recheck and lock the source and selected companion through insertion.
	// Ascending IDs match DeleteEntryPair, avoiding a source/companion deadlock.
	rows, err := tx.QueryContext(ctx, `
		SELECT id, voided FROM entries
		 WHERE id=$1 OR (linked_entry_id=$1 AND person_id=$2 AND unit_price=$4::numeric
		                AND ($3::numeric=0 OR quantity=$3::numeric))
		 ORDER BY id FOR SHARE`, sourceEntryID, companionPersonID, companionHours, companionRate)
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
		`INSERT INTO recurring_entries (neighbor_id, template, interval_kind, next_run, ends_on)
		 VALUES ($1,$2,$3,$4,$5)`, neighborID, blob, intervalKind, nextRun, endsOn); err != nil {
		return err
	}
	return tx.Commit()
}

// ListRecurring returns all rules with their neighbor name, newest first.
func (s *Store) ListRecurring(ctx context.Context) ([]models.RecurringEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT re.id, re.neighbor_id, n.name, re.template, re.interval_kind,
		        re.next_run, re.ends_on, re.active, re.created_at, re.last_run_at,
		        re.last_error, re.last_error_at
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
		var last, endsOn, errAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.NeighborID, &r.NeighborName, &blob, &r.IntervalKind,
			&r.NextRun, &endsOn, &r.Active, &r.CreatedAt, &last, &r.LastError, &errAt); err != nil {
			return nil, err
		}
		if errAt.Valid {
			r.LastErrorAt = &errAt.Time
		}
		if err := json.Unmarshal(blob, &r.Template); err != nil {
			return nil, err
		}
		if last.Valid {
			r.LastRunAt = &last.Time
		}
		if endsOn.Valid {
			r.EndsOn = &endsOn.Time
		}
		r.Upcoming = recurringPreview(r.NextRun, r.IntervalKind, r.EndsOn, 6)
		out = append(out, r)
	}
	return out, rows.Err()
}

// ToggleRecurring flips a rule's active flag and returns the new state, so the
// caller can audit "paused" vs "resumed". ErrNotFound when the id doesn't exist.
func (s *Store) ToggleRecurring(ctx context.Context, id int64) (active bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`UPDATE recurring_entries
		    SET active = NOT active
		  WHERE id=$1 AND (active OR ends_on IS NULL OR ends_on >= GREATEST(next_run,current_date))
		  RETURNING active`, id).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if qerr := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM recurring_entries WHERE id=$1)`, id).Scan(&exists); qerr != nil {
			return false, qerr
		}
		if exists {
			return false, ErrRecurringEnded
		}
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

// recurringPreview calculates future dates with the exact cadence function used
// by the runner and stops at the inclusive end date.
func recurringPreview(start time.Time, kind string, endsOn *time.Time, limit int) []time.Time {
	if limit <= 0 {
		return nil
	}
	out := make([]time.Time, 0, limit)
	for next := start; len(out) < limit; next = advanceDate(next, kind) {
		if endsOn != nil && next.After(*endsOn) {
			break
		}
		out = append(out, next)
	}
	return out
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
//
// Rules are independent: one rule that cannot book (its neighbor's invoice is
// issued, the neighbor was erased, a key collides) waits WITHOUT advancing and
// records why in last_error, while every other rule still runs. Only
// infrastructure errors are returned — joined, after all rules had their turn.
func (s *Store) RunDueRecurring(ctx context.Context) (int, error) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()) // local midnight, not UTC
	// Deterministic order: the oldest due work first, then by id. Without it an
	// arbitrary subset of rules could be starved behind a failing one.
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, neighbor_id, template, interval_kind, next_run, ends_on
		   FROM recurring_entries WHERE active AND next_run <= $1::date
		  ORDER BY next_run, id`, today.Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	type due struct {
		id, neighborID int64
		tmpl           models.RecurTemplate
		kind           string
		next           time.Time
		endsOn         *time.Time
	}
	var list []due
	for rows.Next() {
		var d due
		var blob []byte
		var endsOn sql.NullTime
		if err := rows.Scan(&d.id, &d.neighborID, &blob, &d.kind, &d.next, &endsOn); err != nil {
			_ = rows.Close()
			return 0, err
		}
		if err := json.Unmarshal(blob, &d.tmpl); err != nil {
			_ = rows.Close()
			return 0, err
		}
		// PostgreSQL DATE values arrive at UTC midnight. Recurrence is a local
		// calendar concept; comparing that instant to local midnight skips today's
		// rule in positive UTC offsets. Rebuild the same calendar day locally.
		year, month, day := d.next.Date()
		d.next = time.Date(year, month, day, 0, 0, 0, 0, today.Location())
		if endsOn.Valid {
			year, month, day = endsOn.Time.Date()
			end := time.Date(year, month, day, 0, 0, 0, 0, today.Location())
			d.endsOn = &end
		}
		list = append(list, d)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	created := 0
	var errs []error
	for _, d := range list {
		n, err := s.runDueRule(ctx, d.id, d.neighborID, d.tmpl, d.kind, d.next, d.endsOn, today)
		created += n
		if err != nil {
			// Infrastructure failure for THIS rule: keep going so one rule can
			// never starve the others; the tick still reports it.
			slog.Error("recurring rule failed", "rule", d.id, "neighbor", d.neighborID, "err", err)
			errs = append(errs, fmt.Errorf("recurring rule %d: %w", d.id, err))
		}
	}
	return created, errors.Join(errs...)
}

// recurringBlockedMetric counts occurrences that had to wait on a business
// condition (the rule's waiting state, not a failed tick).
const recurringBlockedMetric = "treckrr_recurring_rules_blocked_total"

// recurringWaitReason classifies an occurrence error as the rule's waiting
// state: a condition of the target account that the operator resolves (storno,
// reopen, add the neighbor again), not an infrastructure failure. The text is
// shown on the Serien page.
func recurringWaitReason(err error) (string, bool) {
	switch {
	case errors.Is(err, ErrInvoiceLocked):
		return "Rechnung ist festgeschrieben – Buchung wartet, bis die Rechnung storniert ist.", true
	case errors.Is(err, ErrNeighborAnonymized):
		return "Nachbar wurde anonymisiert – es werden keine Buchungen mehr erzeugt.", true
	case errors.Is(err, ErrIdempotencyConflict):
		return "Buchungskennung ist bereits anderweitig vergeben – bitte Serie prüfen.", true
	case errors.Is(err, ErrYearCompleted):
		return "Abrechnungsjahr ist abgeschlossen – Buchung wartet.", true
	case errors.Is(err, ErrNotFound):
		return "Nachbar ist im Abrechnungsjahr nicht (mehr) vorhanden – Buchung wartet.", true
	case errors.Is(err, ErrSourceCompanionUnavailable), errors.Is(err, ErrBookingKindLocked):
		return "Vorlage passt nicht mehr zu den Stammdaten – bitte Serie prüfen.", true
	}
	return "", false
}

// runDueRule materializes the due occurrences of ONE rule and persists its
// progress. A business condition stops the rule where it is (next_run is not
// advanced, so the occurrence is retried) and is recorded in last_error; the
// returned error is reserved for infrastructure failures.
func (s *Store) runDueRule(
	ctx context.Context,
	ruleID, neighborID int64,
	tmpl models.RecurTemplate,
	kind string,
	start time.Time,
	endsOn *time.Time,
	today time.Time,
) (int, error) {
	created := 0
	next := start
	var lastRun *time.Time
	waitReason := ""
	// Resolved once per rule, not per occurrence: a catch-up run materializes
	// up to 60 of them and the answer is the same for all.
	companions, personID, err := s.recurringPeople(ctx, tmpl, ruleID)
	if err != nil {
		return 0, err
	}
	var runErr error
	for i := 0; i < 60 && !next.After(today) && (endsOn == nil || !next.After(*endsOn)); i++ { // cap catch-up per rule per tick
		yid, ok, yerr := s.neighborYearForDate(ctx, neighborID, next)
		if yerr != nil {
			runErr = yerr
			break
		}
		if !ok {
			// No open year for this date yet. Stop WITHOUT advancing so the
			// occurrence is retried once that year opens, instead of being
			// skipped past forever (which would silently drop the booking).
			slog.Warn("recurring booking waiting: no open year", "rule", ruleID, "neighbor", neighborID, "date", next.Format("2006-01-02"))
			waitReason = "Kein offenes Abrechnungsjahr für " + next.Format("02.01.2006") + " – Buchung wartet."
			break
		}
		id, cerr := s.materializeRecurring(ctx, recurringOccurrence{
			Template: tmpl, RuleID: ruleID, YearID: yid, NeighborID: neighborID,
			Date: next, Companions: companions, PersonID: personID,
		})
		if cerr != nil {
			if reason, waiting := recurringWaitReason(cerr); waiting {
				metrics.Inc(recurringBlockedMetric)
				slog.Warn("recurring booking waiting", "rule", ruleID, "neighbor", neighborID,
					"date", next.Format("2006-01-02"), "reason", cerr)
				waitReason = reason
			} else {
				runErr = cerr
			}
			break
		}
		if id != 0 {
			created++
		}
		ran := next
		lastRun = &ran
		next = advanceDate(next, kind)
	}
	// Always persist progress, even when a later occurrence failed: the ones
	// before it are booked and must not be retried under a new date. next_run
	// stays unchanged while waiting; last_run_at moves only when an occurrence
	// ran; last_error shows the current waiting state and clears once it runs.
	ended := endsOn != nil && next.After(*endsOn)
	if err := s.saveRuleProgress(ctx, ruleID, next, lastRun, waitReason, runErr == nil, ended); err != nil {
		return created, errors.Join(runErr, err)
	}
	return created, runErr
}

// saveRuleProgress writes next_run/last_run_at when an occurrence ran and, when
// the rule's outcome is known, its waiting reason (” clears it).
func (s *Store) saveRuleProgress(ctx context.Context, ruleID int64, next time.Time, lastRun *time.Time, waitReason string, outcomeKnown, ended bool) error {
	if lastRun != nil || ended {
		var ran any
		if lastRun != nil {
			ran = *lastRun
		}
		if _, err := s.db.ExecContext(ctx, `
			UPDATE recurring_entries
			   SET next_run=$1, last_run_at=COALESCE($2,last_run_at), active=CASE WHEN $3 THEN false ELSE active END
			 WHERE id=$4`, next, ran, ended, ruleID); err != nil {
			return err
		}
	}
	if !outcomeKnown {
		return nil // an infrastructure failure says nothing about the rule itself
	}
	// Written only when the reason changes, so last_error_at tells since when
	// the rule has been waiting for the same thing.
	_, err := s.db.ExecContext(ctx, `
		UPDATE recurring_entries
		   SET last_error = $1::text,
		       last_error_at = CASE WHEN $1::text = '' THEN NULL ELSE now() END
		 WHERE id = $2 AND last_error IS DISTINCT FROM $1::text`, waitReason, ruleID)
	return err
}

// entryFromTemplate rebuilds an Entry from a recurring template (cost recomputed by
// CreateEntry's callers is not needed — the template already carries Cost).
func entryFromTemplate(t models.RecurTemplate) *models.Entry {
	return &models.Entry{
		TaskLabel:           t.TaskLabel,
		Note:                t.Note,
		Unit:                t.Unit,
		Quantity:            t.Quantity,
		UnitPrice:           t.UnitPrice,
		Hours:               t.Hours,
		HourlyRate:          t.HourlyRate,
		FuelAdjustmentLabel: t.FuelAdjustmentLabel,
		FuelAdjustmentPerH:  t.FuelAdjustmentPerH,
		Cost:                t.Cost,
		GespannID:           t.GespannID,
		TractorID:           t.TractorID,
		LoadLevelID:         t.LoadLevelID,
		TractorLabel:        t.TractorLabel,
		LoadLabel:           t.LoadLabel,
		MachineLabels:       t.MachineLabels,
		// A series made FROM a Mannstunden booking keeps its attribution: the
		// template carried the person id but the rebuilt entry dropped it, so
		// every occurrence booked the helper's hours as nobody's.
		PersonID:   t.PersonID,
		PersonName: t.PersonName,
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
	hours := c.Hours
	if !hours.IsPositive() {
		hours = e.Hours // Existing JSON templates intentionally retain same-hours behavior.
	}
	return &models.Entry{
		NeighborID: e.NeighborID, BillingYearID: e.BillingYearID, Date: e.Date,
		TaskLabel: "Mannstunden " + c.Name,
		Unit:      models.UnitMannstunde,
		Quantity:  hours, UnitPrice: c.Rate,
		Cost:     money.Amount(hours, c.Rate),
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

// UpdateRecurringSchedule changes only future scheduling metadata. Already
// materialized bookings and the frozen booking template remain untouched.
func (s *Store) UpdateRecurringSchedule(ctx context.Context, id int64, intervalKind string, nextRun time.Time, endsOn *time.Time) error {
	if intervalKind != "weekly" && intervalKind != "monthly" {
		intervalKind = "weekly"
	}
	if endsOn != nil && endsOn.Before(nextRun) {
		return ErrRecurringEndBeforeStart
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE recurring_entries SET interval_kind=$2, next_run=$3, ends_on=$4 WHERE id=$1`,
		id, intervalKind, nextRun, endsOn)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RecurringSkipResult describes the occurrence deliberately omitted and the
// next effective schedule state.
type RecurringSkipResult struct {
	SkippedOn time.Time
	NextRun   time.Time
	Active    bool
}

// SkipNextRecurring records and advances exactly one scheduled occurrence under
// a row lock. Existing bookings are never changed or removed.
func (s *Store) SkipNextRecurring(ctx context.Context, id int64) (RecurringSkipResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RecurringSkipResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var kind string
	var current time.Time
	var endsOn sql.NullTime
	var active bool
	err = tx.QueryRowContext(ctx, `
		SELECT interval_kind, next_run, ends_on, active
		  FROM recurring_entries WHERE id=$1 FOR UPDATE`, id).
		Scan(&kind, &current, &endsOn, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return RecurringSkipResult{}, ErrNotFound
	}
	if err != nil {
		return RecurringSkipResult{}, err
	}
	if !active {
		return RecurringSkipResult{}, ErrInactiveRule
	}
	next := advanceDate(current, kind)
	remainActive := !endsOn.Valid || !next.After(endsOn.Time)
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recurring_exceptions (recurring_id, skipped_on)
		VALUES ($1,$2) ON CONFLICT (recurring_id, skipped_on) DO NOTHING`, id, current); err != nil {
		return RecurringSkipResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE recurring_entries
		   SET next_run=$2, active=$3, last_error='', last_error_at=NULL
		 WHERE id=$1`, id, next, remainActive); err != nil {
		return RecurringSkipResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecurringSkipResult{}, err
	}
	return RecurringSkipResult{SkippedOn: current, NextRun: next, Active: remainActive}, nil
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
	var endsOn sql.NullTime
	err := s.db.QueryRowContext(ctx,
		`SELECT neighbor_id, template, active, ends_on FROM recurring_entries WHERE id=$1`, id).
		Scan(&neighborID, &blob, &active, &endsOn)
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
	if endsOn.Valid {
		year, month, day := endsOn.Time.Date()
		end := time.Date(year, month, day, 0, 0, 0, 0, today.Location())
		if today.After(end) {
			_, _ = s.db.ExecContext(ctx, `UPDATE recurring_entries SET active=false WHERE id=$1`, id)
			return 0, false, ErrRecurringEnded
		}
	}
	yid, ok, err := s.neighborYearForDate(ctx, neighborID, today)
	if err != nil {
		return 0, false, err
	}
	if !ok {
		return 0, false, nil
	}
	companions, personID, err := s.recurringPeople(ctx, tmpl, id)
	if err != nil {
		return 0, false, err
	}
	entryID, err := s.materializeRecurring(ctx, recurringOccurrence{
		Template: tmpl, RuleID: id, YearID: yid, NeighborID: neighborID,
		Date: today, Companions: companions, PersonID: personID,
	})
	if err != nil {
		return 0, false, err
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE recurring_entries SET last_run_at=$2 WHERE id=$1`, id, today); err != nil {
		return entryID, true, err
	}
	return entryID, true, nil
}
