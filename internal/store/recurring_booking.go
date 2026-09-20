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
)

// LedgerRecurringInput captures a source booking rather than accepting new prices.
type LedgerRecurringInput struct {
	SourceID      int64
	IncludePeople bool
	IntervalKind  string
	NextRun       time.Time
}

// CreateLedgerRecurring freezes a structured posting under its account lock.
func (s *Store) CreateLedgerRecurring(ctx context.Context, in LedgerRecurringInput) error {
	if in.IntervalKind != "weekly" && in.IntervalKind != "monthly" {
		return errors.New("invalid recurring cadence")
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
		`INSERT INTO recurring_entries (neighbor_id,template,interval_kind,next_run) VALUES ($1,$2,$3,$4)`,
		neighborID, blob, in.IntervalKind, in.NextRun); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM persons WHERE id=ANY($1)`, ids)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	live := map[int64]bool{}
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

// recurringOccurrence supplies resolved references without mutating the template.
type recurringOccurrence struct {
	Template   models.RecurTemplate
	RuleID     int64
	YearID     int64
	NeighborID int64
	Date       time.Time
	Companions []models.RecurCompanion
	PersonID   *int64
}

// materializeRecurring preserves accounting direction and per-occurrence retries.
func (s *Store) materializeRecurring(ctx context.Context, occurrence recurringOccurrence) (int64, error) {
	tmpl := occurrence.Template
	key := fmt.Sprintf("recur:%d:%s", occurrence.RuleID, occurrence.Date.Format("2006-01-02"))
	if tmpl.LedgerBooking != nil {
		return s.CreateLedgerBooking(ctx, LedgerBookingInput{
			YearID: occurrence.YearID, NeighborID: occurrence.NeighborID, Date: occurrence.Date,
			Incoming: tmpl.Incoming, Booking: *tmpl.LedgerBooking, IdempotencyKey: key,
		})
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
				id, helperID, err := s.CreateEntryPair(ctx, entry, tmpl.MachineIDs, helper)
				if id == 0 {
					id = helperID
				}
				return id, err
			}
		}
		return s.CreateEntry(ctx, entry, tmpl.MachineIDs)
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
			Cost: hours.Mul(companion.Rate).Round(2), TaskLabel: "Mannstunden " + companion.Name,
			PersonName: companion.Name,
		}
		if companion.PersonID > 0 {
			personID := companion.PersonID
			helper.PersonID = &personID
		}
		helpers = append(helpers, helper)
	}
	id, helperIDs, err := s.CreateEntryGroup(ctx, entry, tmpl.MachineIDs, helpers)
	if id == 0 {
		for _, helperID := range helperIDs {
			if helperID != 0 {
				id = helperID
				break
			}
		}
	}
	return id, err
}
