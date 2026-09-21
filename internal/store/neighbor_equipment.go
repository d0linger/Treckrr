package store

import (
	"context"

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

// ListNeighborEquipment returns legacy neighbor-specific equipment for GDPR
// export. New bookings use the shared price-basis machine catalog instead.
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
