package store

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

// ---- Auswertungen (Ausbaukarte 83/84/85) -----------------------------------

// MachineUsage reports recorded hours with the component rates frozen when the
// booking was saved. Estimated is true when at least one legacy booking in the
// row predates component snapshots and therefore uses today's catalog values.
type MachineUsage struct {
	Name      string
	Hours     decimal.Decimal
	Rate      decimal.Decimal // weighted average contribution per hour
	SelfCost  decimal.Decimal // weighted average own cost per hour
	Revenue   decimal.Decimal
	Cost      decimal.Decimal
	Margin    decimal.Decimal
	Estimated bool // at least one legacy booking used today's catalog values
	// CostComplete is true only when every aggregated booking has a frozen own
	// cost. A partial zero-cost history must not be presented as a full margin.
	CostComplete bool
}

// HasMargin reports whether self costs are configured for this machine.
func (m MachineUsage) HasMargin() bool { return m.CostComplete }

// MachineUsageForYear aggregates machine hours using immutable snapshots. Only
// legacy entries without component snapshots fall back to current catalog
// values, and their aggregate row is marked Estimated.
func (s *Store) MachineUsageForYear(ctx context.Context, yearID int64, from, to time.Time) ([]MachineUsage, error) {
	var fromArg, toArg any
	if !from.IsZero() {
		fromArg = from
	}
	if !to.IsZero() {
		toArg = to
	}
	rows, err := s.db.QueryContext(ctx,
		`WITH usage AS (
		    SELECT s.machine_label AS name, e.hours,
		           s.hourly_rate AS rate, s.self_cost_per_h AS self_cost, false AS estimated
		      FROM entry_machine_snapshots s
		      JOIN entries e ON e.id = s.entry_id
		     WHERE e.billing_year_id = $1 AND NOT e.voided
		       AND ($2::date IS NULL OR e.entry_date >= $2)
		       AND ($3::date IS NULL OR e.entry_date <= $3)
		    UNION ALL
		    SELECT m.name, e.hours, round(m.working_width * m.cost_per_ab, 4),
		           m.self_cost_per_h, true
		      FROM entry_machines em
		      JOIN entries e ON e.id = em.entry_id
		      JOIN machines m ON m.id = em.machine_id
		     WHERE e.billing_year_id = $1 AND NOT e.voided
		       AND ($2::date IS NULL OR e.entry_date >= $2)
		       AND ($3::date IS NULL OR e.entry_date <= $3)
		       AND NOT EXISTS (
		           SELECT 1 FROM entry_machine_snapshots s WHERE s.entry_id = e.id
		       )
		)
		SELECT name, COALESCE(SUM(hours),0),
		       COALESCE(SUM(hours * rate),0), COALESCE(SUM(hours * self_cost),0),
		       bool_or(estimated), bool_and(self_cost > 0)
		  FROM usage
		 GROUP BY name
		 ORDER BY COALESCE(SUM(hours),0) DESC, name`,
		yearID, fromArg, toArg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineUsage
	for rows.Next() {
		var u MachineUsage
		if err := rows.Scan(&u.Name, &u.Hours, &u.Revenue, &u.Cost, &u.Estimated, &u.CostComplete); err != nil {
			return nil, err
		}
		u.Revenue = u.Revenue.Round(2)
		u.Cost = u.Cost.Round(2)
		if u.Hours.IsPositive() {
			u.Rate = u.Revenue.Div(u.Hours).Round(4)
			u.SelfCost = u.Cost.Div(u.Hours).Round(4)
		}
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
