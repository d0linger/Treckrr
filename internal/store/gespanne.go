package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/d0linger/treckrr/internal/models"
)

// ListGespanne returns the gespanne of a base with their machine ids.
func (s *Store) ListGespanne(ctx context.Context, baseID int64) ([]models.Gespann, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, base_id, name, tractor_id, load_level_id, sort_order FROM gespanne WHERE base_id=$1 ORDER BY sort_order, name`, baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Gespann
	for rows.Next() {
		g, err := scanGespann(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		ids, err := s.gespannMachineIDs(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].MachineIDs = ids
	}
	return out, nil
}

// GetGespann returns a single gespann with its machine ids.
func (s *Store) GetGespann(ctx context.Context, id int64) (*models.Gespann, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, base_id, name, tractor_id, load_level_id, sort_order FROM gespanne WHERE id=$1`, id)
	g, err := scanGespann(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	ids, err := s.gespannMachineIDs(ctx, g.ID)
	if err != nil {
		return nil, err
	}
	g.MachineIDs = ids
	return &g, nil
}

func (s *Store) gespannMachineIDs(ctx context.Context, gespannID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT machine_id FROM gespann_machines WHERE gespann_id=$1`, gespannID)
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

// CreateGespann inserts a gespann and links its machines.
func (s *Store) CreateGespann(ctx context.Context, baseID int64, name string, tractorID, loadLevelID *int64, machineIDs []int64, sortOrder int) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO gespanne (base_id,name,tractor_id,load_level_id,sort_order) VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		baseID, name, nullInt(tractorID), nullInt(loadLevelID), sortOrder).Scan(&id); err != nil {
		return 0, err
	}
	for _, mid := range machineIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gespann_machines (gespann_id,machine_id) VALUES ($1,$2)`, id, mid); err != nil {
			return 0, err
		}
	}
	return id, tx.Commit()
}

// UpdateGespann replaces a gespann's fields and machine links.
func (s *Store) UpdateGespann(ctx context.Context, id int64, name string, tractorID, loadLevelID *int64, machineIDs []int64, sortOrder int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE gespanne SET name=$1, tractor_id=$2, load_level_id=$3, sort_order=$4 WHERE id=$5`,
		name, nullInt(tractorID), nullInt(loadLevelID), sortOrder, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gespann_machines WHERE gespann_id=$1`, id); err != nil {
		return err
	}
	for _, mid := range machineIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO gespann_machines (gespann_id,machine_id) VALUES ($1,$2)`, id, mid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteGespann removes a gespann.
func (s *Store) DeleteGespann(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM gespanne WHERE id=$1`, id)
	return err
}

type scanner interface{ Scan(...any) error }

func scanGespann(sc scanner) (models.Gespann, error) {
	var (
		g       models.Gespann
		tractor sql.NullInt64
		load    sql.NullInt64
	)
	if err := sc.Scan(&g.ID, &g.BaseID, &g.Name, &tractor, &load, &g.SortOrder); err != nil {
		return g, err
	}
	if tractor.Valid {
		g.TractorID = &tractor.Int64
	}
	if load.Valid {
		g.LoadLevelID = &load.Int64
	}
	return g, nil
}

func nullInt(p *int64) sql.NullInt64 {
	if p == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: *p, Valid: true}
}
