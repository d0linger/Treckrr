package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ErrBookingKindLocked protects the identity and accounting direction of edits.
var ErrBookingKindLocked = errors.New("booking kind or direction cannot be changed")

// lockBookingKeys serializes retry keys across both storage kinds and accounts.
// Keys are sorted before locking so a paired booking cannot invert lock order.
func lockBookingKeys(ctx context.Context, tx *sql.Tx, keys ...string) error {
	sort.Strings(keys)
	for _, key := range keys {
		if key != "" {
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 1))`, key); err != nil {
				return err
			}
		}
	}
	return nil
}

func ledgerBookingPersonIDs(booking models.LedgerBooking) []int64 {
	ids := make([]int64, 0, len(booking.People)+1)
	if booking.PersonID != nil {
		ids = append(ids, *booking.PersonID)
	}
	for _, person := range booking.People {
		if person.PersonID != nil {
			ids = append(ids, *person.PersonID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	unique := ids[:0]
	for _, id := range ids {
		if len(unique) > 0 && id == unique[len(unique)-1] {
			continue
		}
		unique = append(unique, id)
	}
	return unique
}

// lockLedgerBookingPeople keeps JSON-only person references synchronized with
// DeletePerson. Sorting and deduplicating IDs gives concurrent multi-person
// bookings one stable row-lock order.
func lockLedgerBookingPeople(ctx context.Context, tx *sql.Tx, booking models.LedgerBooking) error {
	for _, id := range ledgerBookingPersonIDs(booking) {
		var lockedID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM persons WHERE id=$1 FOR KEY SHARE`, id).Scan(&lockedID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// rejectLedgerReplay prevents an entry retry key from resolving to a counterclaim.
func rejectLedgerReplay(ctx context.Context, tx *sql.Tx, key string) error {
	if key == "" {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM neighbor_ledger WHERE idempotency_key=$1)`, key).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrIdempotencyConflict
	}
	return nil
}

// protectStructuredLedger extends invoice freezing to direct store callers of
// legacy edit/void/delete methods, without changing settlement-only postings.
// The caller already owns the year/account and posting locks in canonical order.
func protectStructuredLedger(ctx context.Context, tx *sql.Tx, id, yearID, neighborID int64) error {
	var locked bool
	err := tx.QueryRowContext(ctx, `SELECT booking IS NOT NULL AND EXISTS(
		SELECT 1 FROM invoices WHERE billing_year_id=$2 AND neighbor_id=$3 AND kind='invoice' AND status='issued')
		FROM neighbor_ledger WHERE id=$1`, id, yearID, neighborID).Scan(&locked)
	if err != nil {
		return err
	}
	if locked {
		return ErrInvoiceLocked
	}
	return nil
}

// LedgerBookingInput describes a signed counterclaim or fixed account position.
// Amount is computed from Booking, never accepted as a separate client total.
type LedgerBookingInput struct {
	YearID         int64
	NeighborID     int64
	Date           time.Time
	Incoming       bool
	Booking        models.LedgerBooking
	IdempotencyKey string
}

// ledgerBookingValues validates storage invariants and derives the signed amount.
func ledgerBookingValues(in LedgerBookingInput) (decimal.Decimal, []byte, error) {
	b := in.Booking
	validKind := b.Kind == "equipment" || b.Kind == "labor" || b.Kind == "quantity" || b.Kind == "fixed"
	if b.Version != 1 || !validKind || (!in.Incoming && b.Kind != "fixed") {
		return decimal.Zero, nil, errors.New("invalid ledger booking kind")
	}
	if !b.Quantity.IsPositive() || !b.UnitPrice.IsPositive() || b.PersonHours.IsNegative() || b.PersonRate.IsNegative() {
		return decimal.Zero, nil, errors.New("invalid ledger booking price")
	}
	if b.PersonHours.IsPositive() && (!b.PersonRate.IsPositive() || b.PartnerPerson == "") {
		return decimal.Zero, nil, errors.New("incomplete companion booking")
	}
	seenPeople := make(map[int64]bool, len(b.People))
	for _, person := range b.People {
		if person.ID <= 0 || seenPeople[person.ID] || person.Name == "" || !person.Hours.IsPositive() || !person.Rate.IsPositive() {
			return decimal.Zero, nil, errors.New("invalid booking person")
		}
		seenPeople[person.ID] = true
	}
	amount := b.Total()
	if !amount.IsPositive() || amount.GreaterThanOrEqual(decimal.NewFromInt(10_000_000_000)) {
		return decimal.Zero, nil, errors.New("ledger booking amount out of range")
	}
	if in.Incoming {
		amount = amount.Neg()
	}
	blob, err := json.Marshal(b)
	return amount, blob, err
}

// CreateLedgerBooking saves a structured position once, with immutable retry
// semantics: changing account, direction, type, date or content is not a replay.
func (s *Store) CreateLedgerBooking(ctx context.Context, in LedgerBookingInput) (int64, error) {
	amount, blob, err := ledgerBookingValues(in)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockBookingKeys(ctx, tx, in.IdempotencyKey); err != nil {
		return 0, err
	}
	if in.IdempotencyKey != "" {
		var entryExists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entries WHERE idempotency_key=$1)`, in.IdempotencyKey).Scan(&entryExists); err != nil {
			return 0, err
		}
		if entryExists {
			return 0, ErrIdempotencyConflict
		}
		var same bool
		err := tx.QueryRowContext(ctx, `SELECT billing_year_id=$2 AND neighbor_id=$3 AND amount=$4 AND posting_date=$5 AND booking=$6::jsonb
			FROM neighbor_ledger WHERE idempotency_key=$1`, in.IdempotencyKey, in.YearID, in.NeighborID, amount, in.Date, blob).Scan(&same)
		if err == nil {
			if !same {
				return 0, ErrIdempotencyConflict
			}
			return 0, tx.Commit()
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if err := lockMutableBookingAccount(ctx, tx, in.YearID, in.NeighborID); err != nil {
		return 0, err
	}
	if err := lockLedgerBookingPeople(ctx, tx, in.Booking); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO neighbor_ledger
		(billing_year_id, neighbor_id, amount, description, posting_date, booking, idempotency_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		in.YearID, in.NeighborID, amount, in.Booking.Summary(), in.Date, blob, nullStr(in.IdempotencyKey)).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := addAuditTx(ctx, tx, "ledger_add", "ledger", strconv.FormatInt(id, 10), ledgerAuditState(amount, in.Date, false)); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateLedgerBooking changes a structured position without losing its snapshot
// or crossing into invoice-bearing entries. Legacy/carry rows cannot be converted.
func (s *Store) UpdateLedgerBooking(ctx context.Context, id int64, in LedgerBookingInput) error {
	amount, blob, err := ledgerBookingValues(in)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := ledgerAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	var storedKind string
	var incoming bool
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(booking->>'kind',''), amount<0 FROM neighbor_ledger WHERE id=$1 FOR UPDATE`, id).Scan(&storedKind, &incoming); err != nil {
		return err
	}
	if storedKind != in.Booking.Kind || incoming != in.Incoming {
		return ErrBookingKindLocked
	}
	res, err := tx.ExecContext(ctx, `UPDATE neighbor_ledger SET amount=$2, description=$3, posting_date=$4, booking=$5
		WHERE id=$1 AND booking IS NOT NULL AND transfer_id=''`, id, amount, in.Booking.Summary(), in.Date, blob)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := addAuditTx(ctx, tx, "ledger_update", "ledger", strconv.FormatInt(id, 10), fmt.Sprintf("structured; %s", ledgerAuditState(amount, in.Date, false))); err != nil {
		return err
	}
	return tx.Commit()
}
