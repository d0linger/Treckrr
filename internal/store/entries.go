package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Neighbors ----------------------------------------------------------

// ListNeighbors returns all neighbors (active first, then archived).
func (s *Store) ListNeighbors(ctx context.Context) ([]models.Neighbor, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, note, address, tax_id, archived, anonymized, created_at FROM neighbors ORDER BY archived, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Neighbor
	for rows.Next() {
		var n models.Neighbor
		if err := rows.Scan(&n.ID, &n.Name, &n.Note, &n.Address, &n.TaxID, &n.Archived, &n.Anonymized, &n.Created); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNeighbor returns a neighbor by id.
func (s *Store) GetNeighbor(ctx context.Context, id int64) (*models.Neighbor, error) {
	var n models.Neighbor
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, note, address, tax_id, archived, anonymized, created_at FROM neighbors WHERE id=$1`, id).
		Scan(&n.ID, &n.Name, &n.Note, &n.Address, &n.TaxID, &n.Archived, &n.Anonymized, &n.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// AnonymizeNeighbor erases the live personal data of a neighbor (DSGVO Art. 17)
// while keeping the row and its bookings/invoices for the legal retention period.
// The name is replaced with a stable non-identifying placeholder (kept unique for
// the UNIQUE(name) constraint), and the neighbor is archived. Frozen invoice
// snapshots are deliberately untouched. No-op if already anonymized.
func (s *Store) AnonymizeNeighbor(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE neighbors
		    SET name = 'anonymisiert #' || id,
		        note = '', address = '', tax_id = '',
		        archived = TRUE, anonymized = TRUE
		  WHERE id = $1 AND NOT anonymized`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Either the neighbor is gone or was already anonymized; distinguish so the
		// handler can 404 vs. treat it as a no-op.
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM neighbors WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// SetNeighborArchived archives or reactivates a neighbor.
func (s *Store) SetNeighborArchived(ctx context.Context, id int64, archived bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE neighbors SET archived=$1 WHERE id=$2`, archived, id)
	return err
}

// SimilarEntryExists reports whether a non-voided booking with the same named
// task already exists for this neighbor+year on the given date — a strong
// duplicate signal used to warn (not block) before a second identical entry. An
// empty task never matches (too weak a signal to warn on).
func (s *Store) SimilarEntryExists(ctx context.Context, neighborID, yearID int64, date time.Time, task string) (bool, error) {
	if strings.TrimSpace(task) == "" {
		return false, nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM entries
		   WHERE neighbor_id=$1 AND billing_year_id=$2 AND NOT voided
		     AND entry_date=$3 AND task_label=$4)`,
		neighborID, yearID, date, task).Scan(&exists)
	return exists, err
}

// CreateNeighbor inserts a neighbor.
func (s *Store) CreateNeighbor(ctx context.Context, name, note string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbors (name, note) VALUES ($1,$2) RETURNING id`, name, note).Scan(&id)
	return id, err
}

// UpdateNeighbor updates a neighbor.
func (s *Store) UpdateNeighbor(ctx context.Context, id int64, name, note, address, taxID string) error {
	// Never re-populate personal fields on an anonymized neighbor (DSGVO Art. 17):
	// the UI hides the edit form, and this WHERE clause is the server-side backstop
	// against a crafted POST reviving erased data.
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbors SET name=$1, note=$2, address=$3, tax_id=$4 WHERE id=$5 AND NOT anonymized`,
		name, note, address, taxID, id)
	return err
}

// DeleteNeighbor removes a neighbor and their entries.
func (s *Store) DeleteNeighbor(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM neighbors WHERE id=$1`, id)
	return err
}

// CountYearsForNeighbor returns how many billing years a neighbor is part of.
func (s *Store) CountYearsForNeighbor(ctx context.Context, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM billing_year_neighbors WHERE neighbor_id=$1`, neighborID).Scan(&n)
	return n, err
}

// AnyNeighbors reports whether at least one neighbor exists (onboarding state).
func (s *Store) AnyNeighbors(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM neighbors)`).Scan(&exists)
	return exists, err
}

// CountEntriesForNeighbor returns the total entries a neighbor has (all years).
func (s *Store) CountEntriesForNeighbor(ctx context.Context, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM entries WHERE neighbor_id=$1`, neighborID).Scan(&n)
	return n, err
}

// AnyEntries reports whether any booking exists at all (across every year), so
// the first-run onboarding checklist doesn't reappear in a fresh, empty year.
func (s *Store) AnyEntries(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries)`).Scan(&exists)
	return exists, err
}

// ---- Entries -------------------------------------------------------------

// CreateEntry inserts a booked work entry and links its machines.
// ensureUnit fills the unit fields for an hour booking, so a caller that only
// set Hours/HourlyRate still stores a consistent unit='h' row (quantity = hours,
// unit price = hourly rate) rather than an empty unit / zero quantity.
func ensureUnit(e *models.Entry) {
	if e.Unit == "" {
		e.Unit = "h"
		e.Quantity = e.Hours
		e.UnitPrice = e.HourlyRate
	}
}

func (s *Store) CreateEntry(ctx context.Context, e *models.Entry, machineIDs []int64) (int64, error) {
	ensureUnit(e)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO entries
		   (neighbor_id, billing_year_id, entry_date, task_label, gespann_id, tractor_id, load_level_id,
		    tractor_label, load_label, machine_labels, hours, hourly_rate, cost, note,
		    unit, quantity, unit_price, idempotency_key)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		 ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		 RETURNING id`,
		e.NeighborID, e.BillingYearID, e.Date, e.TaskLabel, nullInt(e.GespannID), nullInt(e.TractorID),
		nullInt(e.LoadLevelID), e.TractorLabel, e.LoadLabel, e.MachineLabels, e.Hours,
		e.HourlyRate, e.Cost, e.Note, e.Unit, e.Quantity, e.UnitPrice, nullStr(e.IdempotencyKey)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// A replayed offline booking whose key already exists: a safe no-op. Commit
		// the empty tx and return 0 to signal "already recorded".
		return 0, tx.Commit()
	}
	if err != nil {
		return 0, err
	}
	for _, mid := range machineIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entry_machines (entry_id,machine_id) VALUES ($1,$2)`, id, mid); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// DeleteEntry removes an entry.
func (s *Store) DeleteEntry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE id=$1`, id)
	return err
}

// EntryMachineIDs returns the machine ids linked to an entry (for edit prefill).
func (s *Store) EntryMachineIDs(ctx context.Context, entryID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT machine_id FROM entry_machines WHERE entry_id=$1 AND machine_id IS NOT NULL`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// EntryMachineIDsByNeighborYear returns the machine ids of a neighbor's
// non-voided bookings in a year, grouped by entry id — a single query so the
// beleg's Kostengrundlage doesn't fan out one lookup per booking.
func (s *Store) EntryMachineIDsByNeighborYear(ctx context.Context, neighborID, yearID int64) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT em.entry_id, em.machine_id
		  FROM entry_machines em
		  JOIN entries e ON e.id = em.entry_id
		 WHERE e.neighbor_id = $1 AND e.billing_year_id = $2
		   AND NOT e.voided AND em.machine_id IS NOT NULL`, neighborID, yearID)
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

// GetEntry returns an entry by id.
func (s *Store) GetEntry(ctx context.Context, id int64) (*models.Entry, error) {
	row := s.db.QueryRowContext(ctx, entrySelect+` WHERE id=$1`, id)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ListEntries returns entries for a neighbor within a billing year.
func (s *Store) ListEntries(ctx context.Context, neighborID, yearID int64) ([]models.Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		entrySelect+` WHERE neighbor_id=$1 AND billing_year_id=$2 ORDER BY entry_date, id`,
		neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEntries(rows)
}

// ListEntriesByYear returns all entries within a billing year for export.
func (s *Store) ListEntriesByYear(ctx context.Context, yearID int64) ([]models.Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		entrySelect+` WHERE billing_year_id=$1 ORDER BY neighbor_id, entry_date, id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEntries(rows)
}

// NeighborTotal returns the summed cost and hours for a neighbor in a year,
// excluding voided (canceled) entries.
func (s *Store) NeighborTotal(ctx context.Context, neighborID, yearID int64) (cost, hours decimal.Decimal, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost),0), COALESCE(SUM(hours),0)
		   FROM entries WHERE neighbor_id=$1 AND billing_year_id=$2 AND NOT voided`, neighborID, yearID).
		Scan(&cost, &hours)
	return
}

// YearPaymentTotals returns the received (paid) and outstanding (open) totals for
// a billing year in a single query: paid = the sum of recorded payments, open =
// the sum of each neighbor's remaining balance (net − payments). Replaces a
// per-neighbor fan-out of NeighborTotal calls.
func (s *Store) YearPaymentTotals(ctx context.Context, yearID int64) (paid, open, credit decimal.Decimal, err error) {
	// Per neighbor: net = work bookings + signed ledger postings; paid = recorded
	// payments. Aggregate each side in scalar subqueries first so joining can't
	// multiply rows. "open" clamps each neighbor's remainder at 0 (GREATEST) so a
	// credit does not silently cancel another neighbor's genuine debt; the netted
	// credit is returned separately as "credit".
	err = s.db.QueryRowContext(ctx, `
		WITH per_neighbor AS (
		  SELECT
		    COALESCE((SELECT SUM(e.cost) FROM entries e
		               WHERE e.neighbor_id = byn.neighbor_id
		                 AND e.billing_year_id = byn.billing_year_id
		                 AND NOT e.voided), 0)
		    + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		                 WHERE l.neighbor_id = byn.neighbor_id
		                   AND l.billing_year_id = byn.billing_year_id
		                   AND NOT l.voided), 0) AS net,
		    COALESCE((SELECT SUM(p.amount) FROM payments p
		               WHERE p.neighbor_id = byn.neighbor_id
		                 AND p.billing_year_id = byn.billing_year_id AND p.deleted_at IS NULL), 0) AS paid
		  FROM billing_year_neighbors byn
		  WHERE byn.billing_year_id = $1
		)
		SELECT COALESCE(SUM(paid), 0),
		       COALESCE(SUM(GREATEST(net - paid, 0)), 0),
		       COALESCE(SUM(GREATEST(paid - net, 0)), 0)
		FROM per_neighbor`, yearID).Scan(&paid, &open, &credit)
	return
}

// YearNeighborSummary is one dashboard row for a neighbor in a billing year:
// the net owed (non-voided bookings + signed ledger), hours, the entry count
// (voided included, matching CountEntriesForNeighborYear) and the payment flag.
type YearNeighborSummary struct {
	NeighborID int64
	Name       string
	Cost       decimal.Decimal // net owed (bookings + signed ledger)
	Hours      decimal.Decimal
	Entries    int
	PaidAmount decimal.Decimal // sum of recorded payments
	Remaining  decimal.Decimal // Cost − PaidAmount
	Paid       bool            // fully settled (Remaining <= 0)
}

// YearNeighborSummaries returns one row per neighbor in the year in a single
// query, replacing the dashboard's per-neighbor NeighborTotal +
// CountEntriesForNeighborYear + NeighborLedgerSum fan-out (2+3N round-trips).
// Aggregates ride in scalar subqueries so joins can't multiply rows. Ordered by
// name to match the dashboard list.
func (s *Store) YearNeighborSummaries(ctx context.Context, yearID int64) ([]YearNeighborSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.name,
		  COALESCE((SELECT SUM(e.cost) FROM entries e
		             WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0)
		  + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		               WHERE l.neighbor_id = n.id AND l.billing_year_id = byn.billing_year_id
		                 AND NOT l.voided), 0) AS net,
		  COALESCE((SELECT SUM(e.hours) FROM entries e
		             WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0) AS hours,
		  (SELECT count(*) FROM entries e
		             WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id) AS entries,
		  COALESCE((SELECT SUM(p.amount) FROM payments p
		             WHERE p.neighbor_id = n.id AND p.billing_year_id = byn.billing_year_id AND p.deleted_at IS NULL), 0) AS paid
		FROM billing_year_neighbors byn
		JOIN neighbors n ON n.id = byn.neighbor_id
		WHERE byn.billing_year_id = $1
		ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YearNeighborSummary
	for rows.Next() {
		var r YearNeighborSummary
		if err := rows.Scan(&r.NeighborID, &r.Name, &r.Cost, &r.Hours, &r.Entries, &r.PaidAmount); err != nil {
			return nil, err
		}
		r.Remaining = r.Cost.Sub(r.PaidAmount)
		r.Paid = !r.Remaining.IsPositive() // fully settled when nothing remains
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateEntry replaces the editable fields (and pricing snapshot) of an entry
// and its machine links.
func (s *Store) UpdateEntry(ctx context.Context, e *models.Entry, machineIDs []int64) error {
	ensureUnit(e)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE entries SET entry_date=$1, task_label=$2, gespann_id=$3, tractor_id=$4,
			load_level_id=$5, tractor_label=$6, load_label=$7, machine_labels=$8,
			hours=$9, hourly_rate=$10, cost=$11, note=$12,
			unit=$13, quantity=$14, unit_price=$15 WHERE id=$16`,
		e.Date, e.TaskLabel, nullInt(e.GespannID), nullInt(e.TractorID), nullInt(e.LoadLevelID),
		e.TractorLabel, e.LoadLabel, e.MachineLabels, e.Hours, e.HourlyRate, e.Cost, e.Note,
		e.Unit, e.Quantity, e.UnitPrice, e.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entry_machines WHERE entry_id=$1`, e.ID); err != nil {
		return err
	}
	for _, mid := range machineIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entry_machines (entry_id,machine_id) VALUES ($1,$2)`, e.ID, mid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetEntryVoided cancels or restores an entry (kept for traceability).
func (s *Store) SetEntryVoided(ctx context.Context, id int64, voided bool, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE entries SET voided=$1, void_reason=$2 WHERE id=$3`, voided, reason, id)
	return err
}

const entrySelect = `SELECT id, neighbor_id, billing_year_id, entry_date, task_label, gespann_id,
	tractor_id, load_level_id, tractor_label, load_label, machine_labels,
	hours, hourly_rate, cost, note, voided, void_reason, created_at,
	unit, quantity, unit_price FROM entries`

func collectEntries(rows *sql.Rows) ([]models.Entry, error) {
	var out []models.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEntry(sc scanner) (models.Entry, error) {
	var (
		e       models.Entry
		gespann sql.NullInt64
		tractor sql.NullInt64
		load    sql.NullInt64
		date    time.Time
	)
	if err := sc.Scan(&e.ID, &e.NeighborID, &e.BillingYearID, &date, &e.TaskLabel, &gespann,
		&tractor, &load, &e.TractorLabel, &e.LoadLabel, &e.MachineLabels,
		&e.Hours, &e.HourlyRate, &e.Cost, &e.Note, &e.Voided, &e.VoidReason, &e.Created,
		&e.Unit, &e.Quantity, &e.UnitPrice); err != nil {
		return e, err
	}
	e.Date = date
	if gespann.Valid {
		e.GespannID = &gespann.Int64
	}
	if tractor.Valid {
		e.TractorID = &tractor.Int64
	}
	if load.Valid {
		e.LoadLevelID = &load.Int64
	}
	return e, nil
}
