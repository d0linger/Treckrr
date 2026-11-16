package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/money"
)

// LedgerRecurringInput captures a source booking rather than accepting new prices.
type LedgerRecurringInput struct {
	SourceID      int64
	IncludePeople bool
	IntervalKind  string
	NextRun       time.Time
	EndsOn        *time.Time
}

// CreateLedgerRecurring freezes a structured posting under its account lock.
func (s *Store) CreateLedgerRecurring(ctx context.Context, in LedgerRecurringInput) error {
	if in.IntervalKind != "weekly" && in.IntervalKind != "monthly" {
		return errors.New("invalid recurring cadence")
	}
	if in.EndsOn != nil && in.EndsOn.Before(in.NextRun) {
		return ErrRecurringEndBeforeStart
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := ledgerAccount(ctx, tx, in.SourceID)
	if err != nil {
		return err
	}
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	var raw []byte
	var voided, incoming bool
	err = tx.QueryRowContext(ctx, `SELECT booking,voided,amount<0 FROM neighbor_ledger
		WHERE id=$1 AND booking IS NOT NULL AND transfer_id='' FOR SHARE`, in.SourceID).
		Scan(&raw, &voided, &incoming)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if voided {
		return ErrSourceEntryVoided
	}
	var booking models.LedgerBooking
	if err := json.Unmarshal(raw, &booking); err != nil {
		return err
	}
	if !in.IncludePeople && booking.Kind != "labor" {
		booking.People = nil
		booking.PartnerPerson = ""
		booking.PersonHours = decimal.Zero
		booking.PersonRate = decimal.Zero
	}
	tmpl := models.RecurTemplate{LedgerBooking: &booking, Incoming: incoming}
	blob, err := json.Marshal(tmpl)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO recurring_entries (neighbor_id,template,interval_kind,next_run,ends_on) VALUES ($1,$2,$3,$4,$5)`,
		neighborID, blob, in.IntervalKind, in.NextRun, in.EndsOn); err != nil {
		return err
	}
	return tx.Commit()
}

// validateRecurringGroup refuses changed, removed or canceled source components.
func validateRecurringGroup(ctx context.Context, tx *sql.Tx, sourceID int64, selected []models.RecurCompanion) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,voided,person_id,quantity,unit_price
		FROM entries WHERE id=$1 OR linked_entry_id=$1 ORDER BY id FOR SHARE`, sourceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	found := map[int64]bool{}
	for rows.Next() {
		var id int64
		var voided bool
		var personID sql.NullInt64
		var hours, rate decimal.Decimal
		if err := rows.Scan(&id, &voided, &personID, &hours, &rate); err != nil {
			return err
		}
		if id == sourceID {
			continue
		}
		for _, companion := range selected {
			if id == companion.EntryID && !voided && personID.Int64 == companion.PersonID &&
				hours.Equal(companion.Hours) && rate.Equal(companion.Rate) {
				found[id] = true
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(found) != len(selected) {
		return ErrSourceCompanionUnavailable
	}
	return nil
}

// recurringPeople keeps new snapshot helpers even when their optional ID is gone.
// Legacy single-helper templates retain their established missing-person behavior.
func (s *Store) recurringPeople(ctx context.Context, t models.RecurTemplate, ruleID int64) ([]models.RecurCompanion, *int64, error) {
	if t.LedgerBooking != nil {
		return nil, nil, nil // JSON snapshots deliberately survive deleted master data.
	}
	if t.Companions == nil {
		companion, personID, err := s.liveRefs(ctx, t, ruleID)
		if err != nil || companion == nil {
			return nil, personID, err
		}
		return []models.RecurCompanion{*companion}, personID, nil
	}
	companions := append([]models.RecurCompanion{}, t.Companions...)
	ids := []int64{}
	for _, companion := range companions {
		if companion.PersonID > 0 {
			ids = append(ids, companion.PersonID)
		}
	}
	if t.PersonID != nil {
		ids = append(ids, *t.PersonID)
	}
	live, err := s.existingPersonIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for i := range companions {
		if !live[companions[i].PersonID] {
			companions[i].PersonID = 0
		}
	}
	personID := t.PersonID
	if personID != nil && !live[*personID] {
		personID = nil
	}
	return companions, personID, nil
}

// existingPersonIDs shares the lookup while each persisted recurring format
// keeps its own policy for missing people and frozen helper snapshots.
func (s *Store) existingPersonIDs(ctx context.Context, ids []int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM persons WHERE id=ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	live := make(map[int64]bool, len(ids))
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		live[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return live, nil
}

// recurringOccurrence supplies resolved references without mutating the template.
type recurringOccurrence struct {
	Template   models.RecurTemplate
	RuleID     int64
	YearID     int64
	NeighborID int64
	Date       time.Time
	Companions []models.RecurCompanion
	PersonID   *int64
	// ScheduledFrom is the rule's next_run the runner selected it with; nil
	// for "jetzt buchen", which does not follow the schedule.
	ScheduledFrom *time.Time
}

// errRecurringChanged stops an occurrence whose rule was paused, deleted or
// rescheduled after the runner read it. Nothing was booked.
var errRecurringChanged = errors.New("recurring rule changed since it was selected")

// recurringRunKey carries the rule an occurrence is booked for into the
// booking transaction (see confirmRecurringRun).
type recurringRunKey struct{}

type recurringRun struct {
	ruleID        int64
	scheduledFrom *time.Time
}

// confirmRecurringRun rechecks, inside the booking transaction and under its
// account lock, that the rule an occurrence is booked for is still active and
// unchanged. FOR SHARE holds off a concurrent pause, delete or reschedule
// until the booking commits, so "paused" means no further occurrence. A
// booking without a recurring run in its context is not affected.
func confirmRecurringRun(ctx context.Context, tx *sql.Tx) error {
	run, ok := ctx.Value(recurringRunKey{}).(recurringRun)
	if !ok {
		return nil
	}
	var current bool
	err := tx.QueryRowContext(ctx, `SELECT active AND ($2::date IS NULL OR next_run=$2::date)
		FROM recurring_entries WHERE id=$1 FOR SHARE`, run.ruleID, run.scheduledFrom).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !current) {
		return errRecurringChanged
	}
	return err
}

// materializeRecurring preserves accounting direction and per-occurrence retries.
func (s *Store) materializeRecurring(ctx context.Context, occurrence recurringOccurrence) (int64, error) {
	ctx = context.WithValue(ctx, recurringRunKey{}, recurringRun{
		ruleID: occurrence.RuleID, scheduledFrom: occurrence.ScheduledFrom,
	})
	tmpl := occurrence.Template
	key := fmt.Sprintf("recur:%d:%s", occurrence.RuleID, occurrence.Date.Format("2006-01-02"))
	if tmpl.LedgerBooking != nil {
		ledger := &LedgerBookingInput{
			YearID: occurrence.YearID, NeighborID: occurrence.NeighborID, Date: occurrence.Date,
			Incoming: tmpl.Incoming, Booking: *tmpl.LedgerBooking, IdempotencyKey: key,
		}
		result, err := s.CreateBooking(ctx, BookingCommand{Ledger: ledger})
		return result.MainID, err
	}
	entry := entryFromTemplate(tmpl)
	entry.PersonID = occurrence.PersonID
	entry.NeighborID = occurrence.NeighborID
	entry.BillingYearID = occurrence.YearID
	entry.Date = occurrence.Date
	entry.IdempotencyKey = key
	if tmpl.Companions == nil {
		if len(occurrence.Companions) > 0 {
			if helper := companionEntry(&occurrence.Companions[0], entry); helper != nil {
				result, err := s.CreateBooking(ctx, BookingCommand{
					Entry:      entry,
					MachineIDs: tmpl.MachineIDs,
					Helpers:    []*models.Entry{helper},
				})
				id := result.MainID
				if id == 0 && len(result.HelperIDs) > 0 {
					id = result.HelperIDs[0]
				}
				return id, err
			}
		}
		result, err := s.CreateBooking(ctx, BookingCommand{Entry: entry, MachineIDs: tmpl.MachineIDs})
		return result.MainID, err
	}
	helpers := []*models.Entry{}
	for _, companion := range occurrence.Companions {
		hours := companion.Hours
		if !hours.IsPositive() {
			hours = entry.Hours
		}
		if !hours.IsPositive() || !companion.Rate.IsPositive() {
			return 0, errors.New("invalid recurring helper snapshot")
		}
		helper := &models.Entry{
			NeighborID: entry.NeighborID, BillingYearID: entry.BillingYearID, Date: entry.Date,
			Unit: models.UnitMannstunde, Quantity: hours, UnitPrice: companion.Rate,
			Cost: money.Amount(hours, companion.Rate), TaskLabel: "Mannstunden " + companion.Name,
			PersonName: companion.Name,
		}
		if companion.PersonID > 0 {
			personID := companion.PersonID
			helper.PersonID = &personID
		}
		helpers = append(helpers, helper)
	}
	result, err := s.CreateBooking(ctx, BookingCommand{
		Entry:      entry,
		MachineIDs: tmpl.MachineIDs,
		Helpers:    helpers,
	})
	id := result.MainID
	if id == 0 {
		for _, helperID := range result.HelperIDs {
			if helperID != 0 {
				id = helperID
				break
			}
		}
	}
	return id, err
}
