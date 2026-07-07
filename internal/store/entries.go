package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// ---- Neighbors ----------------------------------------------------------

// ListNeighbors returns all neighbors (active first, then archived).
func (s *Store) ListNeighbors(ctx context.Context) ([]models.Neighbor, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, note, archived, created_at FROM neighbors ORDER BY archived, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Neighbor
	for rows.Next() {
		var n models.Neighbor
		if err := rows.Scan(&n.ID, &n.Name, &n.Note, &n.Archived, &n.Created); err != nil {
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
		`SELECT id, name, note, archived, created_at FROM neighbors WHERE id=$1`, id).
		Scan(&n.ID, &n.Name, &n.Note, &n.Archived, &n.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// SetNeighborArchived archives or reactivates a neighbor.
func (s *Store) SetNeighborArchived(ctx context.Context, id int64, archived bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE neighbors SET archived=$1 WHERE id=$2`, archived, id)
	return err
}

// CreateNeighbor inserts a neighbor.
func (s *Store) CreateNeighbor(ctx context.Context, name, note string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbors (name, note) VALUES ($1,$2) RETURNING id`, name, note).Scan(&id)
	return id, err
}

// UpdateNeighbor updates a neighbor.
func (s *Store) UpdateNeighbor(ctx context.Context, id int64, name, note string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbors SET name=$1, note=$2 WHERE id=$3`, name, note, id)
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

// CountEntriesForNeighbor returns the total entries a neighbor has (all years).
func (s *Store) CountEntriesForNeighbor(ctx context.Context, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM entries WHERE neighbor_id=$1`, neighborID).Scan(&n)
	return n, err
}

// ---- Entries -------------------------------------------------------------

// CreateEntry inserts a booked work entry and links its machines.
func (s *Store) CreateEntry(ctx context.Context, e *models.Entry, machineIDs []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO entries
		   (neighbor_id, billing_year_id, entry_date, task_label, gespann_id, tractor_id, load_level_id,
		    tractor_label, load_label, machine_labels, hours, hourly_rate, cost, note)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
		e.NeighborID, e.BillingYearID, e.Date, e.TaskLabel, nullInt(e.GespannID), nullInt(e.TractorID),
		nullInt(e.LoadLevelID), e.TractorLabel, e.LoadLabel, e.MachineLabels, e.Hours,
		e.HourlyRate, e.Cost, e.Note).Scan(&id)
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

// YearPaymentTotals returns the paid and open cost totals for a billing year in
// a single query (paid = neighbors marked paid, open = the rest). This replaces
// a per-neighbor fan-out of NeighborTotal calls.
func (s *Store) YearPaymentTotals(ctx context.Context, yearID int64) (paid, open decimal.Decimal, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN byn.paid THEN e.cost ELSE 0 END), 0),
		  COALESCE(SUM(CASE WHEN NOT byn.paid THEN e.cost ELSE 0 END), 0)
		FROM billing_year_neighbors byn
		LEFT JOIN entries e
		  ON e.neighbor_id = byn.neighbor_id
		 AND e.billing_year_id = byn.billing_year_id
		 AND NOT e.voided
		WHERE byn.billing_year_id = $1`, yearID).Scan(&paid, &open)
	return
}

// UpdateEntry replaces the editable fields (and pricing snapshot) of an entry
// and its machine links.
func (s *Store) UpdateEntry(ctx context.Context, e *models.Entry, machineIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE entries SET entry_date=$1, task_label=$2, gespann_id=$3, tractor_id=$4,
			load_level_id=$5, tractor_label=$6, load_label=$7, machine_labels=$8,
			hours=$9, hourly_rate=$10, cost=$11, note=$12 WHERE id=$13`,
		e.Date, e.TaskLabel, nullInt(e.GespannID), nullInt(e.TractorID), nullInt(e.LoadLevelID),
		e.TractorLabel, e.LoadLabel, e.MachineLabels, e.Hours, e.HourlyRate, e.Cost, e.Note, e.ID); err != nil {
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
	hours, hourly_rate, cost, note, voided, void_reason, created_at FROM entries`

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
		&e.Hours, &e.HourlyRate, &e.Cost, &e.Note, &e.Voided, &e.VoidReason, &e.Created); err != nil {
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
