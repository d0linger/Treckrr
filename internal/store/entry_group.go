package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/d0linger/treckrr/internal/models"
)

// MaxEntryCompanions bounds one booking's additional people and transaction size.
const MaxEntryCompanions = 20

// ErrInvalidEntryGroup reports an invalid or internally inconsistent group.
var ErrInvalidEntryGroup = errors.New("invalid entry group")

// EntryCompanions returns all directly linked helper snapshots, including voided
// rows retained for traceability, in stable ID order. It never follows siblings.
func (s *Store) EntryCompanions(ctx context.Context, id int64) ([]models.Entry, error) {
	rows, err := s.db.QueryContext(ctx, entrySelect+` WHERE linked_entry_id=$1 ORDER BY id`, id)
	if err != nil {
		return nil, fmt.Errorf("query entry companions: %w", err)
	}
	defer rows.Close()
	return collectEntries(rows)
}

// validateEntryGroup checks structural ownership before acquiring write locks.
// Detailed user-facing validation belongs to the form parser; these invariants
// also protect direct callers, recurrence, and offline replay.
func validateEntryGroup(e *models.Entry, helpers []*models.Entry, creating bool) error {
	if e == nil || e.NeighborID <= 0 || e.BillingYearID <= 0 || e.LinkedEntryID != nil ||
		len(helpers) > MaxEntryCompanions || (creating && e.ID != 0) || (!creating && e.ID <= 0) {
		return ErrInvalidEntryGroup
	}
	seen := make(map[int64]bool, len(helpers))
	for _, h := range helpers {
		if h == nil || h.NeighborID != e.NeighborID || h.BillingYearID != e.BillingYearID ||
			h.Unit != models.UnitMannstunde || h.Date.Format("2006-01-02") != e.Date.Format("2006-01-02") ||
			h.ID < 0 || (h.ID != 0 && (creating || h.ID == e.ID || seen[h.ID])) ||
			(h.ID == 0 && h.Voided) || (h.LinkedEntryID != nil && *h.LinkedEntryID != e.ID) ||
			!h.Quantity.IsPositive() || h.UnitPrice.IsNegative() ||
			!h.Quantity.Equal(h.Quantity.Round(4)) || !h.UnitPrice.Equal(h.UnitPrice.Round(4)) ||
			(h.PersonID == nil && strings.TrimSpace(h.PersonName) == "") {
			return ErrInvalidEntryGroup
		}
		if h.ID != 0 {
			seen[h.ID] = true
		}
	}
	return nil
}

// entryGroupCopies freezes fallback replay identity without modifying caller
// structs. Supplied form fingerprints already cover active inputs, independent
// of subsequent master-data rate changes, and therefore take precedence.
func entryGroupCopies(e *models.Entry, machineIDs []int64, helpers []*models.Entry) (*models.Entry, []*models.Entry, error) {
	main := *e
	ensureUnit(&main)
	people := make([]*models.Entry, len(helpers))
	for i, helper := range helpers {
		copy := *helper
		copy.Cost = copy.Quantity.Mul(copy.UnitPrice).Round(2)
		people[i] = &copy
	}
	if main.IdempotencyKey == "" {
		for _, helper := range people {
			if helper.IdempotencyKey != "" {
				return nil, nil, ErrIdempotencyConflict
			}
		}
		return &main, people, nil
	}
	if main.RequestFingerprint == "" {
		ids := slices.Clone(machineIDs)
		slices.Sort(ids)
		blob, err := json.Marshal(struct {
			Main     models.Entry
			Machines []int64
			Helpers  []*models.Entry
		}{main, ids, people})
		if err != nil {
			return nil, nil, fmt.Errorf("fingerprint entry group: %w", err)
		}
		sum := sha256.Sum256(blob)
		main.RequestFingerprint = hex.EncodeToString(sum[:])
	}
	keyHash := sha256.Sum256([]byte(main.IdempotencyKey))
	seen := map[string]bool{main.IdempotencyKey: true}
	for i, helper := range people {
		if helper.IdempotencyKey == "" {
			helper.IdempotencyKey = "group-person:" + hex.EncodeToString(keyHash[:]) + ":" + strconv.Itoa(i)
		}
		if seen[helper.IdempotencyKey] {
			return nil, nil, ErrIdempotencyConflict
		}
		seen[helper.IdempotencyKey] = true
		helper.RequestFingerprint = main.RequestFingerprint
	}
	return &main, people, nil
}

// entryGroupReplay captures each keyed row before deciding whether any writes
// are required. A complete replay remains a no-op even after invoice issuance.
type entryGroupReplay struct {
	id     int64
	linked *int64
	exists bool
}

// readEntryGroupReplay rejects reused keys and links to unrelated bookings.
func readEntryGroupReplay(ctx context.Context, tx *sql.Tx, e *models.Entry, helpers []*models.Entry) ([]entryGroupReplay, error) {
	entries := append([]*models.Entry{e}, helpers...)
	states := make([]entryGroupReplay, len(entries))
	for i, entry := range entries {
		var err error
		state := &states[i]
		state.id, state.linked, state.exists, err = existingEntryForReplay(
			ctx, tx, entry.IdempotencyKey, entry.RequestFingerprint, e.BillingYearID, e.NeighborID)
		if err != nil {
			return nil, err
		}
		if i == 0 && state.linked != nil {
			return nil, ErrIdempotencyConflict
		}
		if i > 0 && state.linked != nil && (!states[0].exists || *state.linked != states[0].id) {
			return nil, ErrIdempotencyConflict
		}
	}
	return states, nil
}

// CreateEntryGroup atomically records a primary entry with zero or more people.
// Primary entries can be hourly, quantity-based, or labor. Returned helper IDs
// correspond to input order; zero denotes an already recorded row. Deterministic
// retry keys repair deleted members without repricing surviving snapshots.
func (s *Store) CreateEntryGroup(ctx context.Context, e *models.Entry, machineIDs []int64, helpers []*models.Entry) (int64, []int64, error) {
	if err := validateEntryGroup(e, helpers, true); err != nil {
		return 0, nil, err
	}
	main, people, err := entryGroupCopies(e, machineIDs, helpers)
	if err != nil {
		return 0, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	keys := []string{main.IdempotencyKey}
	for _, person := range people {
		keys = append(keys, person.IdempotencyKey)
	}
	if err := lockBookingKeys(ctx, tx, keys...); err != nil {
		return 0, nil, err
	}
	states, err := readEntryGroupReplay(ctx, tx, main, people)
	if err != nil {
		return 0, nil, err
	}
	complete := states[0].exists
	for _, state := range states[1:] {
		complete = complete && state.exists && state.linked != nil
	}
	helperIDs := make([]int64, len(people))
	if complete {
		return 0, helperIDs, tx.Commit()
	}
	if err := lockMutableBookingAccount(ctx, tx, main.BillingYearID, main.NeighborID); err != nil {
		return 0, nil, err
	}
	var mainID int64
	if !states[0].exists {
		mainID, err = insertEntryTx(ctx, tx, main, machineIDs)
		if err != nil {
			return 0, nil, err
		}
	}
	linkID := mainID
	if linkID == 0 {
		linkID = states[0].id
	}
	if linkID == 0 {
		return 0, nil, ErrIdempotencyConflict
	}
	for i, person := range people {
		person.LinkedEntryID = &linkID
		var id int64
		if !states[i+1].exists {
			id, err = insertEntryTx(ctx, tx, person, nil)
			if err != nil {
				return 0, nil, err
			}
		}
		helperIDs[i] = id
		if id == 0 && states[i+1].linked == nil {
			if _, err := tx.ExecContext(ctx, `UPDATE entries SET linked_entry_id=$1
				WHERE id=$2 AND linked_entry_id IS NULL AND unit=$3
				  AND neighbor_id=$4 AND billing_year_id=$5`, linkID, states[i+1].id,
				models.UnitMannstunde, main.NeighborID, main.BillingYearID); err != nil {
				return 0, nil, err
			}
		}
	}
	if err := addAuditTx(ctx, tx, "entry_group_add", "entry", strconv.FormatInt(linkID, 10),
		fmt.Sprintf("created_main=%d; created_people=%v", mainID, helperIDs)); err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return mainID, helperIDs, nil
}

// lockedEntryGroup reads the primary row and all direct members in the same ID
// lock order as recurrence and deletion, after taking the account boundary.
func lockedEntryGroup(ctx context.Context, tx *sql.Tx, e *models.Entry) (models.Entry, map[int64]models.Entry, error) {
	rows, err := tx.QueryContext(ctx, entrySelect+` WHERE (id=$1 OR linked_entry_id=$1)
		AND billing_year_id=$2 AND neighbor_id=$3 ORDER BY id FOR UPDATE`, e.ID, e.BillingYearID, e.NeighborID)
	if err != nil {
		return models.Entry{}, nil, err
	}
	defer rows.Close()
	entries, err := collectEntries(rows)
	if err != nil {
		return models.Entry{}, nil, err
	}
	var main models.Entry
	people := make(map[int64]models.Entry, len(entries))
	for _, entry := range entries {
		if entry.ID == e.ID {
			main = entry
		} else {
			if entry.Unit != models.UnitMannstunde {
				return main, nil, ErrInvalidEntryGroup
			}
			people[entry.ID] = entry
		}
	}
	if main.ID == 0 || main.LinkedEntryID != nil {
		return main, nil, ErrNotFound
	}
	return main, people, nil
}

// UpdateEntryGroup updates one booking and its people in a single transaction.
// Existing IDs must be direct members of this account's group. Missing members
// are voided, not deleted; submitted voided members can be restored explicitly.
// The primary row's void state and every original replay fingerprint stay intact.
func (s *Store) UpdateEntryGroup(ctx context.Context, e *models.Entry, machineIDs []int64, helpers []*models.Entry) error {
	if err := validateEntryGroup(e, helpers, false); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockMutableBookingAccount(ctx, tx, e.BillingYearID, e.NeighborID); err != nil {
		return err
	}
	before, stored, err := lockedEntryGroup(ctx, tx, e)
	if err != nil {
		return err
	}
	for _, person := range helpers {
		if person.ID != 0 {
			if _, exists := stored[person.ID]; !exists {
				return ErrNotFound
			}
		}
	}
	if err := updateEntryTx(ctx, tx, e, machineIDs); err != nil {
		return err
	}
	for _, helper := range helpers {
		person := *helper
		person.LinkedEntryID = &e.ID
		person.Cost = person.Quantity.Mul(person.UnitPrice).Round(2)
		if person.ID == 0 {
			// Additions during edits are not offline creations. Retrying an edit
			// replaces/voids its previous additions; it must never claim an old key.
			person.IdempotencyKey, person.RequestFingerprint = "", ""
			id, err := insertEntryTx(ctx, tx, &person, nil)
			if err != nil {
				return err
			}
			person.ID = id
		} else {
			if err := updateEntryTx(ctx, tx, &person, nil); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE entries SET voided=$2, void_reason=$3 WHERE id=$1`,
				person.ID, person.Voided, person.VoidReason); err != nil {
				return err
			}
			delete(stored, person.ID)
		}
		if err := addAuditTx(ctx, tx, "entry_group_person_update", "entry", strconv.FormatInt(person.ID, 10),
			fmt.Sprintf("group=%d; quantity=%s; rate=%s; cost=%s; voided=%t", e.ID,
				person.Quantity.String(), person.UnitPrice.String(), person.Cost.String(), person.Voided)); err != nil {
			return err
		}
	}
	for id, person := range stored {
		if person.Voided {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE entries SET voided=true, void_reason=$2 WHERE id=$1`,
			id, "Aus Buchung entfernt"); err != nil {
			return err
		}
		if err := addAuditTx(ctx, tx, "entry_group_person_void", "entry", strconv.FormatInt(id, 10),
			fmt.Sprintf("group=%d; cost=%s", e.ID, person.Cost.String())); err != nil {
			return err
		}
	}
	if err := addAuditTx(ctx, tx, "entry_group_update", "entry", strconv.FormatInt(e.ID, 10),
		fmt.Sprintf("cost=%s->%s; people=%d; voided=%t", before.Cost.String(), e.Cost.String(), len(helpers), before.Voided)); err != nil {
		return err
	}
	return tx.Commit()
}
