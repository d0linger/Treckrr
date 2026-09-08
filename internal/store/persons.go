package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Personenstamm (Ausbaukarte 57) ---------------------------------------
//
// Helpers with their own hourly rate, so Mannstunden come out of the master
// data instead of a hand-typed price on every booking. Persons are archived,
// never silently dropped: their name is printed on billed documents.

const personCols = `id, name, hourly_rate, note, archived, created_at`

func scanPerson(sc scanner) (models.Person, error) {
	var p models.Person
	err := sc.Scan(&p.ID, &p.Name, &p.HourlyRate, &p.Note, &p.Archived, &p.Created)
	return p, err
}

// ListPersons returns all helpers, active first, then archived.
func (s *Store) ListPersons(ctx context.Context) ([]models.Person, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+personCols+` FROM persons ORDER BY archived, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ActivePersons returns the helpers offered on the booking forms.
func (s *Store) ActivePersons(ctx context.Context) ([]models.Person, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+personCols+` FROM persons WHERE NOT archived ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPerson returns one helper. ErrNotFound when the id does not exist.
func (s *Store) GetPerson(ctx context.Context, id int64) (*models.Person, error) {
	p, err := scanPerson(s.db.QueryRowContext(ctx, `SELECT `+personCols+` FROM persons WHERE id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CreatePerson adds a helper. The name is UNIQUE; the handler reports a
// clash the same way the neighbor page does, so no extra sentinel is needed.
func (s *Store) CreatePerson(ctx context.Context, name string, rate decimal.Decimal, note string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO persons (name, hourly_rate, note) VALUES ($1,$2,$3) RETURNING id`,
		name, rate, note).Scan(&id)
	return id, err
}

// UpdatePerson changes a helper's name, rate and note.
func (s *Store) UpdatePerson(ctx context.Context, id int64, name string, rate decimal.Decimal, note string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE persons SET name=$2, hourly_rate=$3, note=$4 WHERE id=$1`, id, name, rate, note)
	return err
}

// SetPersonArchived hides a helper from the booking forms without touching the
// bookings that already carry their name.
func (s *Store) SetPersonArchived(ctx context.Context, id int64, archived bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE persons SET archived=$2 WHERE id=$1`, id, archived)
	return err
}

// DeletePerson removes a helper who was never booked. One who was is kept:
// person_id is ON DELETE SET NULL, so deleting would silently orphan the
// attribution on documents that are already out of the house — archive instead.
func (s *Store) DeletePerson(ctx context.Context, id int64) error {
	var booked bool
	if err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM entries WHERE person_id=$1)`, id).Scan(&booked); err != nil {
		return err
	}
	if booked {
		return ErrHasHistory
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM persons WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PersonHours sums the booked Mannstunden per helper for a year — the figure
// behind "wer war wie lange im Einsatz".
type PersonHours struct {
	PersonID int64
	Name     string
	Hours    decimal.Decimal
	Cost     decimal.Decimal
}

// PersonHoursForYear aggregates non-voided person bookings of a billing year.
func (s *Store) PersonHoursForYear(ctx context.Context, yearID int64) ([]PersonHours, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, p.name, COALESCE(SUM(e.quantity),0), COALESCE(SUM(e.cost),0)
		   FROM entries e JOIN persons p ON p.id = e.person_id
		  WHERE e.billing_year_id = $1 AND NOT e.voided
		  GROUP BY p.id, p.name ORDER BY p.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonHours
	for rows.Next() {
		var ph PersonHours
		if err := rows.Scan(&ph.PersonID, &ph.Name, &ph.Hours, &ph.Cost); err != nil {
			return nil, err
		}
		out = append(out, ph)
	}
	return out, rows.Err()
}
