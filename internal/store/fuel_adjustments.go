package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// ListFuelAdjustments returns all versions for a price basis, newest first.
func (s *Store) ListFuelAdjustments(ctx context.Context, baseID int64) ([]models.FuelAdjustment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, base_id, effective_from, label, amount_per_h, created_at, updated_at
		  FROM fuel_adjustments
		 WHERE base_id=$1
		 ORDER BY effective_from DESC, id DESC`, baseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.FuelAdjustment
	for rows.Next() {
		var adjustment models.FuelAdjustment
		if err := rows.Scan(&adjustment.ID, &adjustment.BaseID, &adjustment.EffectiveFrom,
			&adjustment.Label, &adjustment.AmountPerH, &adjustment.Created, &adjustment.Updated); err != nil {
			return nil, err
		}
		out = append(out, adjustment)
	}
	return out, rows.Err()
}

// FuelAdjustmentAt returns the latest version effective on the booking date.
func (s *Store) FuelAdjustmentAt(ctx context.Context, baseID int64, date time.Time) (*models.FuelAdjustment, error) {
	var adjustment models.FuelAdjustment
	err := s.db.QueryRowContext(ctx, `
		SELECT id, base_id, effective_from, label, amount_per_h, created_at, updated_at
		  FROM fuel_adjustments
		 WHERE base_id=$1 AND effective_from <= $2
		 ORDER BY effective_from DESC, id DESC
		 LIMIT 1`, baseID, date).Scan(
		&adjustment.ID, &adjustment.BaseID, &adjustment.EffectiveFrom, &adjustment.Label,
		&adjustment.AmountPerH, &adjustment.Created, &adjustment.Updated,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &adjustment, err
}

// SaveFuelAdjustment creates or updates one version within a price basis.
func (s *Store) SaveFuelAdjustment(ctx context.Context, adjustment *models.FuelAdjustment) error {
	if adjustment.ID == 0 {
		return s.db.QueryRowContext(ctx, `
			INSERT INTO fuel_adjustments (base_id, effective_from, label, amount_per_h)
			VALUES ($1,$2,$3,$4)
			RETURNING id`, adjustment.BaseID, adjustment.EffectiveFrom, adjustment.Label,
			adjustment.AmountPerH).Scan(&adjustment.ID)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE fuel_adjustments
		   SET effective_from=$1, label=$2, amount_per_h=$3, updated_at=now()
		 WHERE id=$4 AND base_id=$5`, adjustment.EffectiveFrom, adjustment.Label,
		adjustment.AmountPerH, adjustment.ID, adjustment.BaseID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteFuelAdjustment removes one version belonging to the selected basis.
func (s *Store) DeleteFuelAdjustment(ctx context.Context, baseID, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM fuel_adjustments WHERE id=$1 AND base_id=$2`, id, baseID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveFuelAdjustment selects the latest applicable version from any
// ordering. It is shared by bulk repricing so it can load all versions once.
func EffectiveFuelAdjustment(adjustments []models.FuelAdjustment, date time.Time) *models.FuelAdjustment {
	var selected *models.FuelAdjustment
	for i := range adjustments {
		candidate := &adjustments[i]
		if candidate.EffectiveFrom.After(date) {
			continue
		}
		if selected == nil || candidate.EffectiveFrom.After(selected.EffectiveFrom) ||
			(candidate.EffectiveFrom.Equal(selected.EffectiveFrom) && candidate.ID > selected.ID) {
			selected = candidate
		}
	}
	return selected
}
