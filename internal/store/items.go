package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"treckrr/internal/models"
)

// ---- Load levels ---------------------------------------------------------

// ListLoadLevels returns the load levels of a base, ordered.
func (s *Store) ListLoadLevels(ctx context.Context, baseID int64) ([]models.LoadLevel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, base_id, name, cost_per_ps, sort_order
		   FROM load_levels WHERE base_id=$1 ORDER BY sort_order, name`, baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LoadLevel
	for rows.Next() {
		var l models.LoadLevel
		if err := rows.Scan(&l.ID, &l.BaseID, &l.Name, &l.CostPerPS, &l.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// GetLoadLevel returns one load level by id.
func (s *Store) GetLoadLevel(ctx context.Context, id int64) (*models.LoadLevel, error) {
	var l models.LoadLevel
	err := s.db.QueryRowContext(ctx,
		`SELECT id, base_id, name, cost_per_ps, sort_order FROM load_levels WHERE id=$1`, id).
		Scan(&l.ID, &l.BaseID, &l.Name, &l.CostPerPS, &l.SortOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

// CreateLoadLevel inserts a load level.
func (s *Store) CreateLoadLevel(ctx context.Context, baseID int64, name string, cost float64, sort int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO load_levels (base_id,name,cost_per_ps,sort_order) VALUES ($1,$2,$3,$4) RETURNING id`,
		baseID, name, cost, sort).Scan(&id)
	return id, err
}

// UpdateLoadLevel updates a load level.
func (s *Store) UpdateLoadLevel(ctx context.Context, id int64, name string, cost float64, sort int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE load_levels SET name=$1, cost_per_ps=$2, sort_order=$3 WHERE id=$4`,
		name, cost, sort, id)
	return err
}

// DeleteLoadLevel removes a load level.
func (s *Store) DeleteLoadLevel(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM load_levels WHERE id=$1`, id)
	return err
}

// ---- Tractors ------------------------------------------------------------

const tractorCols = `id, base_id, ident, name, ps, active, sort_order`

// ListTractors returns all tractors of a base (active and inactive).
func (s *Store) ListTractors(ctx context.Context, baseID int64) ([]models.Tractor, error) {
	return s.queryTractors(ctx,
		`SELECT `+tractorCols+` FROM tractors WHERE base_id=$1 ORDER BY active DESC, sort_order, ident`,
		baseID)
}

// ListActiveTractors returns only the active tractors of a base (for bookings).
func (s *Store) ListActiveTractors(ctx context.Context, baseID int64) ([]models.Tractor, error) {
	return s.queryTractors(ctx,
		`SELECT `+tractorCols+` FROM tractors WHERE base_id=$1 AND active ORDER BY sort_order, ident`,
		baseID)
}

func (s *Store) queryTractors(ctx context.Context, query string, args ...any) ([]models.Tractor, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Tractor
	for rows.Next() {
		var t models.Tractor
		if err := rows.Scan(&t.ID, &t.BaseID, &t.Ident, &t.Name, &t.PS, &t.Active, &t.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTractor returns a tractor by id.
func (s *Store) GetTractor(ctx context.Context, id int64) (*models.Tractor, error) {
	var t models.Tractor
	err := s.db.QueryRowContext(ctx,
		`SELECT `+tractorCols+` FROM tractors WHERE id=$1`, id).
		Scan(&t.ID, &t.BaseID, &t.Ident, &t.Name, &t.PS, &t.Active, &t.SortOrder)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &t, err
}

// SetTractorActive activates or deactivates a tractor.
func (s *Store) SetTractorActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tractors SET active=$1 WHERE id=$2`, active, id)
	return err
}

// CreateTractor inserts a tractor.
func (s *Store) CreateTractor(ctx context.Context, baseID int64, ident, name string, ps float64, sortOrder int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO tractors (base_id,ident,name,ps,sort_order) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		baseID, ident, name, ps, sortOrder).Scan(&id)
	return id, err
}

// UpdateTractor updates a tractor.
func (s *Store) UpdateTractor(ctx context.Context, id int64, ident, name string, ps float64, sortOrder int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tractors SET ident=$1, name=$2, ps=$3, sort_order=$4 WHERE id=$5`, ident, name, ps, sortOrder, id)
	return err
}

// DeleteTractor removes a tractor.
func (s *Store) DeleteTractor(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tractors WHERE id=$1`, id)
	return err
}

// ---- Machines ------------------------------------------------------------

const machineCols = `id, base_id, name, working_width, cost_per_ab, active, category, sort_order`

// ListMachines returns all machines of a base (active and inactive).
func (s *Store) ListMachines(ctx context.Context, baseID int64) ([]models.Machine, error) {
	return s.queryMachines(ctx,
		`SELECT `+machineCols+` FROM machines WHERE base_id=$1 ORDER BY active DESC, sort_order, name`, baseID)
}

// ListActiveMachines returns only the active machines of a base (for bookings).
func (s *Store) ListActiveMachines(ctx context.Context, baseID int64) ([]models.Machine, error) {
	return s.queryMachines(ctx,
		`SELECT `+machineCols+` FROM machines WHERE base_id=$1 AND active ORDER BY sort_order, name`, baseID)
}

func (s *Store) queryMachines(ctx context.Context, query string, args ...any) ([]models.Machine, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Machine
	for rows.Next() {
		var m models.Machine
		if err := rows.Scan(&m.ID, &m.BaseID, &m.Name, &m.WorkingWidth, &m.CostPerAB,
			&m.Active, &m.Category, &m.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMachineActive activates or deactivates a machine.
func (s *Store) SetMachineActive(ctx context.Context, id int64, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET active=$1 WHERE id=$2`, active, id)
	return err
}

// MachineCategories returns the distinct non-empty categories used in a base.
func (s *Store) MachineCategories(ctx context.Context, baseID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT category FROM machines WHERE base_id=$1 AND category <> '' ORDER BY category`, baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// MachinesByIDs returns machines matching the id list, ordered by name.
func (s *Store) MachinesByIDs(ctx context.Context, ids []int64) ([]models.Machine, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "$" + strconv.Itoa(i+1)
		args[i] = id
	}
	query := `SELECT ` + machineCols + ` FROM machines
	           WHERE id IN (` + strings.Join(placeholders, ",") + `) ORDER BY sort_order, name`
	return s.queryMachines(ctx, query, args...)
}

// CreateMachine inserts a machine.
func (s *Store) CreateMachine(ctx context.Context, baseID int64, name string, width, cost float64, category string, sortOrder int) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO machines (base_id,name,working_width,cost_per_ab,category,sort_order)
		 VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		baseID, name, width, cost, category, sortOrder).Scan(&id)
	return id, err
}

// UpdateMachine updates a machine.
func (s *Store) UpdateMachine(ctx context.Context, id int64, name string, width, cost float64, category string, sortOrder int) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE machines SET name=$1, working_width=$2, cost_per_ab=$3, category=$4, sort_order=$5 WHERE id=$6`,
		name, width, cost, category, sortOrder, id)
	return err
}

// DeleteMachine removes a machine.
func (s *Store) DeleteMachine(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM machines WHERE id=$1`, id)
	return err
}
