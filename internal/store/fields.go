package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ErrFieldExists reports an active field of the same name for the neighbor.
var ErrFieldExists = errors.New("field name already in use")

const fieldCols = `id, neighbor_id, name, COALESCE(area_ha, 0), plot_number, archived, created_at`

func scanField(sc scanner) (models.Field, error) {
	var f models.Field
	err := sc.Scan(&f.ID, &f.NeighborID, &f.Name, &f.AreaHa, &f.PlotNumber, &f.Archived, &f.Created)
	return f, err
}

// CreateField adds a field to a neighbor's list.
func (s *Store) CreateField(ctx context.Context, f models.Field) (int64, error) {
	var area any
	if f.AreaHa.IsPositive() {
		area = f.AreaHa
	}
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbor_fields (neighbor_id, name, area_ha, plot_number) VALUES ($1, $2, $3, $4) RETURNING id`,
		f.NeighborID, f.Name, area, f.PlotNumber).Scan(&id)
	if isUniqueViolation(err) {
		return 0, ErrFieldExists
	}
	return id, err
}

// ListFields returns a neighbor's fields, active first, by name.
func (s *Store) ListFields(ctx context.Context, neighborID int64) ([]models.Field, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+fieldCols+` FROM neighbor_fields
		WHERE neighbor_id = $1 ORDER BY archived, lower(name), id`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Field
	for rows.Next() {
		f, err := scanField(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// GetField returns one field of a neighbor; ErrNotFound for another
// neighbor's field.
func (s *Store) GetField(ctx context.Context, neighborID, id int64) (models.Field, error) {
	f, err := scanField(s.db.QueryRowContext(ctx, `SELECT `+fieldCols+` FROM neighbor_fields WHERE id = $1 AND neighbor_id = $2`, id, neighborID))
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// SetFieldArchived hides a field from new bookings or brings it back.
func (s *Store) SetFieldArchived(ctx context.Context, neighborID, id int64, archived bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE neighbor_fields SET archived = $3 WHERE id = $1 AND neighbor_id = $2`, id, neighborID, archived)
	if isUniqueViolation(err) {
		return ErrFieldExists
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteField removes a field no booking names; a used field can only be
// archived, so its bookings keep the link.
func (s *Store) DeleteField(ctx context.Context, neighborID, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM neighbor_fields f WHERE f.id = $1 AND f.neighbor_id = $2
		AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.field_id = f.id)`, id, neighborID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// FieldStat is the year's work on one field.
type FieldStat struct {
	NeighborID   int64
	NeighborName string
	FieldID      *int64
	Label        string
	AreaHa       decimal.Decimal
	Count        int
	Hours        decimal.Decimal
	Cost         decimal.Decimal
}

// PerHa is the cost per hectare, zero without a known area.
func (f FieldStat) PerHa() decimal.Decimal {
	if !f.AreaHa.IsPositive() {
		return decimal.Zero
	}
	return f.Cost.Div(f.AreaHa).Round(2)
}

// FieldStatsForYear sums the non-voided bookings that name a field, per
// neighbor and field, optionally for one neighbor and a period (zero = open);
// most expensive first.
func (s *Store) FieldStatsForYear(ctx context.Context, yearID int64, neighborID *int64, from, to time.Time) ([]FieldStat, error) {
	var fromArg, toArg any
	if !from.IsZero() {
		fromArg = from
	}
	if !to.IsZero() {
		toArg = to
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.neighbor_id, n.name, e.field_id, e.field_label, COALESCE(max(f.area_ha), 0),
		       count(*), COALESCE(sum(e.hours), 0), COALESCE(sum(e.cost), 0)
		  FROM entries e
		  JOIN neighbors n ON n.id = e.neighbor_id
		  LEFT JOIN neighbor_fields f ON f.id = e.field_id
		 WHERE e.billing_year_id = $1 AND NOT e.voided AND e.field_label <> ''
		   AND ($2::bigint IS NULL OR e.neighbor_id = $2)
		   AND ($3::date IS NULL OR e.entry_date >= $3) AND ($4::date IS NULL OR e.entry_date <= $4)
		 GROUP BY e.neighbor_id, n.name, e.field_id, e.field_label
		 ORDER BY sum(e.cost) DESC, n.name, e.field_label`, yearID, nullInt(neighborID), fromArg, toArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FieldStat
	for rows.Next() {
		var f FieldStat
		var fieldID sql.NullInt64
		if err := rows.Scan(&f.NeighborID, &f.NeighborName, &fieldID, &f.Label, &f.AreaHa, &f.Count, &f.Hours, &f.Cost); err != nil {
			return nil, err
		}
		if fieldID.Valid {
			f.FieldID = &fieldID.Int64
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// isUniqueViolation reports a Postgres unique_violation (23505) by its code.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
