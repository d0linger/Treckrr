package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/d0linger/treckrr/internal/models"
)

const neighborEquipmentCols = `id, neighbor_id, name, capacity, capacity_unit, billing_unit, default_rate, note, archived, created_at`

func scanNeighborEquipment(sc scanner) (models.NeighborEquipment, error) {
	var equipment models.NeighborEquipment
	err := sc.Scan(&equipment.ID, &equipment.NeighborID, &equipment.Name,
		&equipment.Capacity, &equipment.CapacityUnit, &equipment.BillingUnit,
		&equipment.DefaultRate, &equipment.Note, &equipment.Archived, &equipment.Created)
	return equipment, err
}

// ListNeighborEquipment returns reusable foreign equipment, active first.
func (s *Store) ListNeighborEquipment(ctx context.Context, neighborID int64) ([]models.NeighborEquipment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+neighborEquipmentCols+`
		FROM neighbor_equipment WHERE neighbor_id=$1 ORDER BY archived, name`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.NeighborEquipment, 0)
	for rows.Next() {
		equipment, err := scanNeighborEquipment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, equipment)
	}
	return out, rows.Err()
}

// ActiveNeighborEquipment returns equipment offered on new booking forms.
func (s *Store) ActiveNeighborEquipment(ctx context.Context, neighborID int64) ([]models.NeighborEquipment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+neighborEquipmentCols+`
		FROM neighbor_equipment WHERE neighbor_id=$1 AND NOT archived ORDER BY name`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]models.NeighborEquipment, 0)
	for rows.Next() {
		equipment, err := scanNeighborEquipment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, equipment)
	}
	return out, rows.Err()
}

// GetNeighborEquipment returns one equipment record.
func (s *Store) GetNeighborEquipment(ctx context.Context, id int64) (*models.NeighborEquipment, error) {
	equipment, err := scanNeighborEquipment(s.db.QueryRowContext(ctx,
		`SELECT `+neighborEquipmentCols+` FROM neighbor_equipment WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &equipment, nil
}

// CreateNeighborEquipment adds reusable equipment for one neighbor.
func (s *Store) CreateNeighborEquipment(ctx context.Context, equipment models.NeighborEquipment) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockPersonalDataNeighbor(ctx, tx, equipment.NeighborID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO neighbor_equipment
		(neighbor_id,name,capacity,capacity_unit,billing_unit,default_rate,note)
		VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`, equipment.NeighborID,
		equipment.Name, equipment.Capacity, equipment.CapacityUnit, equipment.BillingUnit,
		equipment.DefaultRate, equipment.Note).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateNeighborEquipment changes master data without touching booking snapshots.
func (s *Store) UpdateNeighborEquipment(ctx context.Context, equipment models.NeighborEquipment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockPersonalDataNeighbor(ctx, tx, equipment.NeighborID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE neighbor_equipment SET
		name=$3,capacity=$4,capacity_unit=$5,billing_unit=$6,default_rate=$7,note=$8
		WHERE id=$1 AND neighbor_id=$2`, equipment.ID, equipment.NeighborID,
		equipment.Name, equipment.Capacity, equipment.CapacityUnit, equipment.BillingUnit,
		equipment.DefaultRate, equipment.Note)
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
	return tx.Commit()
}

// SetNeighborEquipmentArchived controls whether equipment can be newly selected.
func (s *Store) SetNeighborEquipmentArchived(ctx context.Context, id, neighborID int64, archived bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if !archived {
		if err := lockPersonalDataNeighbor(ctx, tx, neighborID); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `UPDATE neighbor_equipment SET archived=$3 WHERE id=$1 AND neighbor_id=$2`, id, neighborID, archived)
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
	return tx.Commit()
}

// DeleteNeighborEquipment removes unused master data. JSON booking references
// count as history even though they deliberately are not foreign keys.
func (s *Store) DeleteNeighborEquipment(ctx context.Context, id, neighborID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var lockedID int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM neighbor_equipment WHERE id=$1 AND neighbor_id=$2 FOR UPDATE`, id, neighborID).Scan(&lockedID); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var used bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM neighbor_ledger WHERE booking->>'neighbor_equipment_id'=$1)`, strconv.FormatInt(id, 10)).Scan(&used); err != nil {
		return err
	}
	if used {
		return ErrHasHistory
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM neighbor_equipment WHERE id=$1 AND neighbor_id=$2`, id, neighborID); err != nil {
		return err
	}
	return tx.Commit()
}
