package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// ---- Auswertungen (Ausbaukarte 83/84/85) -----------------------------------

// MachineUsage reports recorded hours and estimates their revenue and cost at
// CURRENT machine rates. Bookings snapshot only the combined rig rate, not its
// components; exact historical machine allocations cannot be reconstructed.
// These estimates must not be presented as booked or invoiced revenue.
type MachineUsage struct {
	Name     string
	Hours    decimal.Decimal
	Rate     decimal.Decimal // € per hour this machine contributes
	SelfCost decimal.Decimal // € per hour it costs to run (0 = not configured)
	Revenue  decimal.Decimal
	Cost     decimal.Decimal
	Margin   decimal.Decimal
}

// HasMargin reports whether self costs are configured for this machine.
func (m MachineUsage) HasMargin() bool { return m.SelfCost.IsPositive() }

// MachineUsageForYear aggregates machine hours and current-rate estimates for a
// billing year, optionally narrowed to a date range (zero = unbounded).
func (s *Store) MachineUsageForYear(ctx context.Context, yearID int64, from, to time.Time) ([]MachineUsage, error) {
	var fromArg, toArg any
	if !from.IsZero() {
		fromArg = from
	}
	if !to.IsZero() {
		toArg = to
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.name, COALESCE(SUM(e.hours),0), round(m.working_width * m.cost_per_ab, 2), m.self_cost_per_h
		   FROM entry_machines em
		   JOIN entries e ON e.id = em.entry_id
		   JOIN machines m ON m.id = em.machine_id
		  WHERE e.billing_year_id = $1 AND NOT e.voided
		    AND ($2::date IS NULL OR e.entry_date >= $2)
		    AND ($3::date IS NULL OR e.entry_date <= $3)
		  GROUP BY m.id, m.name, m.working_width, m.cost_per_ab, m.self_cost_per_h
		  ORDER BY COALESCE(SUM(e.hours),0) DESC, m.name`,
		yearID, fromArg, toArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineUsage
	for rows.Next() {
		var u MachineUsage
		if err := rows.Scan(&u.Name, &u.Hours, &u.Rate, &u.SelfCost); err != nil {
			return nil, err
		}
		u.Revenue = u.Hours.Mul(u.Rate).Round(2)
		u.Cost = u.Hours.Mul(u.SelfCost).Round(2)
		u.Margin = u.Revenue.Sub(u.Cost)
		out = append(out, u)
	}
	return out, rows.Err()
}

// UnitMetric is one task measured in its own unit: how much was done and what
// that cost per unit (Ausbaukarte 84 — "je Hektar" only means something once
// the quantity is carried along).
type UnitMetric struct {
	Task     string
	Unit     string
	Quantity decimal.Decimal
	Cost     decimal.Decimal
	PerUnit  decimal.Decimal
	Bookings int
}

// UnitMetricsForYear aggregates non-hour bookings by task and unit.
func (s *Store) UnitMetricsForYear(ctx context.Context, yearID int64, from, to time.Time) ([]UnitMetric, error) {
	var fromArg, toArg any
	if !from.IsZero() {
		fromArg = from
	}
	if !to.IsZero() {
		toArg = to
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(NULLIF(task_label,''), 'Sonstige'), unit,
		        COALESCE(SUM(quantity),0), COALESCE(SUM(cost),0), count(*)
		   FROM entries
		  WHERE billing_year_id = $1 AND NOT voided AND unit <> '' AND unit <> 'h'
		    AND ($2::date IS NULL OR entry_date >= $2)
		    AND ($3::date IS NULL OR entry_date <= $3)
		  GROUP BY 1, 2
		  ORDER BY COALESCE(SUM(cost),0) DESC`,
		yearID, fromArg, toArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UnitMetric
	for rows.Next() {
		var m UnitMetric
		if err := rows.Scan(&m.Task, &m.Unit, &m.Quantity, &m.Cost, &m.Bookings); err != nil {
			return nil, err
		}
		if m.Quantity.IsPositive() {
			m.PerUnit = m.Cost.Div(m.Quantity).Round(2)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// NeighborYearPoint is one year of a neighbor's history, for the multi-year
// trend (Ausbaukarte 85).
type NeighborYearPoint struct {
	YearID int64
	Year   int
	Cost   decimal.Decimal
	Hours  decimal.Decimal
	Paid   decimal.Decimal
}

// NeighborYearSeries returns a neighbor's cost, hours and payments per billing
// year, oldest first — the trend the overview showed only as separate tiles.
func (s *Store) NeighborYearSeries(ctx context.Context, neighborID int64) ([]NeighborYearPoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT y.id, y.year,
		        COALESCE((SELECT SUM(e.cost) FROM entries e
		                   WHERE e.billing_year_id = y.id AND e.neighbor_id = $1 AND NOT e.voided), 0),
		        COALESCE((SELECT SUM(e.hours) FROM entries e
		                   WHERE e.billing_year_id = y.id AND e.neighbor_id = $1 AND NOT e.voided), 0),
		        COALESCE((SELECT SUM(p.amount) FROM payments p
		                   WHERE p.billing_year_id = y.id AND p.neighbor_id = $1 AND p.deleted_at IS NULL), 0)
		   FROM billing_years y
		  WHERE EXISTS (SELECT 1 FROM billing_year_neighbors byn
		                 WHERE byn.billing_year_id = y.id AND byn.neighbor_id = $1)
		  ORDER BY y.year`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NeighborYearPoint
	for rows.Next() {
		var p NeighborYearPoint
		if err := rows.Scan(&p.YearID, &p.Year, &p.Cost, &p.Hours, &p.Paid); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
