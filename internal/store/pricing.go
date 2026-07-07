package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// ---- Price bases ---------------------------------------------------------

// ListBases returns all pricing bases, newest year first.
func (s *Store) ListBases(ctx context.Context) ([]models.PriceBase, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, year, name, locked, created_at FROM price_bases ORDER BY year DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.PriceBase
	for rows.Next() {
		var b models.PriceBase
		if err := rows.Scan(&b.ID, &b.Year, &b.Name, &b.Locked, &b.Created); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBase returns a single pricing basis by id.
func (s *Store) GetBase(ctx context.Context, id int64) (*models.PriceBase, error) {
	var b models.PriceBase
	err := s.db.QueryRowContext(ctx,
		`SELECT id, year, name, locked, created_at FROM price_bases WHERE id=$1`, id).
		Scan(&b.ID, &b.Year, &b.Name, &b.Locked, &b.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

// LatestBase returns the base with the highest year, or ErrNotFound.
func (s *Store) LatestBase(ctx context.Context) (*models.PriceBase, error) {
	var b models.PriceBase
	err := s.db.QueryRowContext(ctx,
		`SELECT id, year, name, locked, created_at FROM price_bases ORDER BY year DESC LIMIT 1`).
		Scan(&b.ID, &b.Year, &b.Name, &b.Locked, &b.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &b, err
}

// SetBaseLocked freezes or unfreezes a base.
func (s *Store) SetBaseLocked(ctx context.Context, id int64, locked bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE price_bases SET locked=$1 WHERE id=$2`, locked, id)
	return err
}

// CreateEmptyBase inserts a bare pricing basis for a year.
func (s *Store) CreateEmptyBase(ctx context.Context, year int, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO price_bases (year, name) VALUES ($1,$2) RETURNING id`, year, name).Scan(&id)
	return id, err
}

// UpdateBase renames a basis and updates its "valid from" year.
func (s *Store) UpdateBase(ctx context.Context, id int64, year int, name string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE price_bases SET year=$1, name=$2 WHERE id=$3`, year, name, id)
	return err
}

// BaseInUse reports whether any billing year references the basis.
func (s *Store) BaseInUse(ctx context.Context, id int64) (bool, error) {
	var inUse bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM billing_years WHERE base_id=$1)`, id).Scan(&inUse)
	return inUse, err
}

// DeleteBase removes a basis and all its items/gespanne. Callers must ensure it
// is not referenced by a billing year first (see BaseInUse).
func (s *Store) DeleteBase(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM price_bases WHERE id=$1`, id)
	return err
}

// CloneBase creates a new base for newYear copying all load levels, tractors,
// machines and gespanne from the source base. The source is left unchanged.
func (s *Store) CloneBase(ctx context.Context, srcBaseID int64, newYear int, name string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var newID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO price_bases (year, name) VALUES ($1,$2) RETURNING id`, newYear, name).
		Scan(&newID); err != nil {
		return 0, fmt.Errorf("create base: %w", err)
	}

	// Copy load levels, remembering old->new id mapping.
	loadMap, err := copyRows(ctx, tx,
		`SELECT id, name, cost_per_ps, sort_order FROM load_levels WHERE base_id=$1`,
		srcBaseID,
		func(scan func(...any) error) (int64, func(int64) (int64, error), error) {
			var oldID int64
			var name string
			var cost decimal.Decimal
			var sort int
			if err := scan(&oldID, &name, &cost, &sort); err != nil {
				return 0, nil, err
			}
			insert := func(nb int64) (int64, error) {
				var nid int64
				err := tx.QueryRowContext(ctx,
					`INSERT INTO load_levels (base_id,name,cost_per_ps,sort_order)
					 VALUES ($1,$2,$3,$4) RETURNING id`, nb, name, cost, sort).Scan(&nid)
				return nid, err
			}
			return oldID, insert, nil
		}, newID)
	if err != nil {
		return 0, fmt.Errorf("copy load levels: %w", err)
	}

	tractorMap, err := copyRows(ctx, tx,
		`SELECT id, ident, name, ps, active, sort_order FROM tractors WHERE base_id=$1`, srcBaseID,
		func(scan func(...any) error) (int64, func(int64) (int64, error), error) {
			var oldID int64
			var ident, name string
			var ps decimal.Decimal
			var active bool
			var sortOrder int
			if err := scan(&oldID, &ident, &name, &ps, &active, &sortOrder); err != nil {
				return 0, nil, err
			}
			insert := func(nb int64) (int64, error) {
				var nid int64
				err := tx.QueryRowContext(ctx,
					`INSERT INTO tractors (base_id,ident,name,ps,active,sort_order) VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
					nb, ident, name, ps, active, sortOrder).Scan(&nid)
				return nid, err
			}
			return oldID, insert, nil
		}, newID)
	if err != nil {
		return 0, fmt.Errorf("copy tractors: %w", err)
	}

	machineMap, err := copyRows(ctx, tx,
		`SELECT id, name, working_width, cost_per_ab, active, category, sort_order FROM machines WHERE base_id=$1`, srcBaseID,
		func(scan func(...any) error) (int64, func(int64) (int64, error), error) {
			var oldID int64
			var name, category string
			var ab, cost decimal.Decimal
			var active bool
			var sortOrder int
			if err := scan(&oldID, &name, &ab, &cost, &active, &category, &sortOrder); err != nil {
				return 0, nil, err
			}
			insert := func(nb int64) (int64, error) {
				var nid int64
				err := tx.QueryRowContext(ctx,
					`INSERT INTO machines (base_id,name,working_width,cost_per_ab,active,category,sort_order)
					 VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, nb, name, ab, cost, active, category, sortOrder).Scan(&nid)
				return nid, err
			}
			return oldID, insert, nil
		}, newID)
	if err != nil {
		return 0, fmt.Errorf("copy machines: %w", err)
	}

	// Copy gespanne, remapping tractor/load references, then their machines.
	grows, err := tx.QueryContext(ctx,
		`SELECT id, name, tractor_id, load_level_id, sort_order FROM gespanne WHERE base_id=$1`, srcBaseID)
	if err != nil {
		return 0, err
	}
	type gcopy struct {
		oldID       int64
		name        string
		tractorID   sql.NullInt64
		loadLevelID sql.NullInt64
		sortOrder   int
	}
	var gs []gcopy
	for grows.Next() {
		var g gcopy
		if err := grows.Scan(&g.oldID, &g.name, &g.tractorID, &g.loadLevelID, &g.sortOrder); err != nil {
			_ = grows.Close()
			return 0, err
		}
		gs = append(gs, g)
	}
	_ = grows.Close()

	for _, g := range gs {
		var newTractor, newLoad sql.NullInt64
		if g.tractorID.Valid {
			if v, ok := tractorMap[g.tractorID.Int64]; ok {
				newTractor = sql.NullInt64{Int64: v, Valid: true}
			}
		}
		if g.loadLevelID.Valid {
			if v, ok := loadMap[g.loadLevelID.Int64]; ok {
				newLoad = sql.NullInt64{Int64: v, Valid: true}
			}
		}
		var newGID int64
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO gespanne (base_id,name,tractor_id,load_level_id,sort_order)
			 VALUES ($1,$2,$3,$4,$5) RETURNING id`, newID, g.name, newTractor, newLoad, g.sortOrder).
			Scan(&newGID); err != nil {
			return 0, fmt.Errorf("copy gespann: %w", err)
		}
		mrows, err := tx.QueryContext(ctx,
			`SELECT machine_id FROM gespann_machines WHERE gespann_id=$1`, g.oldID)
		if err != nil {
			return 0, err
		}
		var oldMachineIDs []int64
		for mrows.Next() {
			var mid int64
			if err := mrows.Scan(&mid); err != nil {
				_ = mrows.Close()
				return 0, err
			}
			oldMachineIDs = append(oldMachineIDs, mid)
		}
		_ = mrows.Close()
		for _, oldM := range oldMachineIDs {
			if newM, ok := machineMap[oldM]; ok {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO gespann_machines (gespann_id,machine_id) VALUES ($1,$2)`,
					newGID, newM); err != nil {
					return 0, err
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newID, nil
}

// copyRows runs a select against the source base and, for each row, inserts a
// copy into newBaseID via the provided closure, returning an old->new id map.
func copyRows(
	ctx context.Context,
	tx *sql.Tx,
	query string,
	srcBaseID int64,
	handle func(scan func(...any) error) (oldID int64, insert func(newBase int64) (int64, error), err error),
	newBaseID int64,
) (map[int64]int64, error) {
	rows, err := tx.QueryContext(ctx, query, srcBaseID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		oldID  int64
		insert func(int64) (int64, error)
	}
	var items []pending
	for rows.Next() {
		oldID, insert, err := handle(rows.Scan)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		items = append(items, pending{oldID: oldID, insert: insert})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	_ = rows.Close()

	mapping := make(map[int64]int64, len(items))
	for _, it := range items {
		newID, err := it.insert(newBaseID)
		if err != nil {
			return nil, err
		}
		mapping[it.oldID] = newID
	}
	return mapping, nil
}
