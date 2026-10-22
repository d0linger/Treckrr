package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/shopspring/decimal"
)

// RateLoad, RateTractor and RateMachine are the validated rows of an imported
// rate list. Active and SelfCostPerH carry over through an export/import round
// trip; SelfCostPerH nil keeps the comparison basis value for that machine.
type RateLoad struct {
	Name      string
	CostPerPS decimal.Decimal
}

// RateTractor is one tractor of an imported rate list.
type RateTractor struct {
	Ident, Name string
	PS          decimal.Decimal
	Active      bool
}

// RateMachine is one implement of an imported rate list.
type RateMachine struct {
	Name, Category   string
	Width, CostPerAB decimal.Decimal
	SelfCostPerH     *decimal.Decimal
	Active           bool
}

// RateList is a complete basis as read from a rate-list file.
type RateList struct {
	Loads    []RateLoad
	Tractors []RateTractor
	Machines []RateMachine
}

// RateListImport reports what CreateBaseFromRateList created.
type RateListImport struct {
	BaseID          int64
	Gespanne        int
	GespanneSkipped []string
}

// rateKey follows the database's case-sensitive per-basis identity. Folding
// case can silently attach a copied rig to a different valid catalog item.
func rateKey(s string) string { return s }

// CreateBaseFromRateList creates a new, unlocked basis from an imported list in
// one transaction. File order becomes the sort order. With a comparison basis,
// machines without an own self-cost value keep that basis' value, and every
// rig whose tractor, load level and machines all exist again (matched by
// Kennung or name) is recreated; the others are reported by name. Existing
// bases, years and bookings are never touched.
func (s *Store) CreateBaseFromRateList(ctx context.Context, year int, name string, list RateList, sourceBaseID int64) (RateListImport, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RateListImport{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var result RateListImport
	if err := tx.QueryRowContext(ctx, `INSERT INTO price_bases (year, name, billing_increment_h, minimum_h)
		SELECT $1, $2, COALESCE(src.billing_increment_h, 0), COALESCE(src.minimum_h, 0)
		  FROM (SELECT 1) one LEFT JOIN price_bases src ON src.id = $3
		RETURNING id`, year, name, sourceBaseID).
		Scan(&result.BaseID); err != nil {
		return RateListImport{}, fmt.Errorf("create base: %w", err)
	}
	loads := map[string]int64{}
	for i, l := range list.Loads {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO load_levels (base_id, name, cost_per_ps, sort_order)
			VALUES ($1, $2, $3, $4) RETURNING id`, result.BaseID, l.Name, l.CostPerPS, i+1).Scan(&id); err != nil {
			return RateListImport{}, fmt.Errorf("load level %q: %w", l.Name, err)
		}
		loads[rateKey(l.Name)] = id
	}
	tractors := map[string]int64{}
	for i, t := range list.Tractors {
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO tractors (base_id, ident, name, ps, active, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, result.BaseID, t.Ident, t.Name, t.PS, t.Active, i+1).Scan(&id); err != nil {
			return RateListImport{}, fmt.Errorf("tractor %q: %w", t.Ident, err)
		}
		tractors[rateKey(t.Ident)] = id
	}
	selfCosts := map[string]decimal.Decimal{}
	if sourceBaseID > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT name, self_cost_per_h FROM machines WHERE base_id = $1`, sourceBaseID)
		if err != nil {
			return RateListImport{}, err
		}
		for rows.Next() {
			var n string
			var cost decimal.Decimal
			if err := rows.Scan(&n, &cost); err != nil {
				_ = rows.Close()
				return RateListImport{}, err
			}
			if _, seen := selfCosts[rateKey(n)]; !seen {
				selfCosts[rateKey(n)] = cost
			}
		}
		if err := rows.Close(); err != nil {
			return RateListImport{}, err
		}
	}
	machines := map[string]int64{}
	for i, m := range list.Machines {
		selfCost := selfCosts[rateKey(m.Name)]
		if m.SelfCostPerH != nil {
			selfCost = *m.SelfCostPerH
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO machines (base_id, name, working_width, cost_per_ab, active, category, sort_order, self_cost_per_h)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			result.BaseID, m.Name, m.Width, m.CostPerAB, m.Active, m.Category, i+1, selfCost).Scan(&id); err != nil {
			return RateListImport{}, fmt.Errorf("machine %q: %w", m.Name, err)
		}
		machines[rateKey(m.Name)] = id
	}
	if sourceBaseID > 0 {
		if err := copyMatchingGespanne(ctx, tx, sourceBaseID, &result, loads, tractors, machines); err != nil {
			return RateListImport{}, err
		}
	}
	return result, tx.Commit()
}

// copyMatchingGespanne recreates the comparison basis' rigs whose components
// all exist in the new basis, matched by Kennung/name.
func copyMatchingGespanne(ctx context.Context, tx *sql.Tx, sourceBaseID int64, result *RateListImport, loads, tractors, machines map[string]int64) error {
	type rig struct {
		name                string
		tractor, load       string
		hasTractor, hasLoad bool
		machines            []string
		sortOrder           int
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.name, g.sort_order, t.ident, l.name,
		       COALESCE(json_agg(m.name ORDER BY m.name) FILTER (WHERE m.id IS NOT NULL), '[]')
		  FROM gespanne g
		  LEFT JOIN tractors t ON t.id = g.tractor_id
		  LEFT JOIN load_levels l ON l.id = g.load_level_id
		  LEFT JOIN gespann_machines gm ON gm.gespann_id = g.id
		  LEFT JOIN machines m ON m.id = gm.machine_id
		 WHERE g.base_id = $1
		 GROUP BY g.id, g.name, g.sort_order, t.ident, l.name
		 ORDER BY g.sort_order, g.id`, sourceBaseID)
	if err != nil {
		return err
	}
	var rigs []rig
	for rows.Next() {
		var id int64
		var r rig
		var tractor, load *string
		var names []byte
		if err := rows.Scan(&id, &r.name, &r.sortOrder, &tractor, &load, &names); err != nil {
			_ = rows.Close()
			return err
		}
		if err := json.Unmarshal(names, &r.machines); err != nil {
			_ = rows.Close()
			return fmt.Errorf("rig %q machines: %w", r.name, err)
		}
		if tractor != nil {
			r.tractor, r.hasTractor = *tractor, true
		}
		if load != nil {
			r.load, r.hasLoad = *load, true
		}
		rigs = append(rigs, r)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, r := range rigs {
		var tractorID, loadID *int64
		ok := true
		if r.hasTractor {
			if id, found := tractors[rateKey(r.tractor)]; found {
				tractorID = &id
			} else {
				ok = false
			}
		}
		if r.hasLoad {
			if id, found := loads[rateKey(r.load)]; found {
				loadID = &id
			} else {
				ok = false
			}
		}
		machineIDs := make([]int64, 0, len(r.machines))
		for _, m := range r.machines {
			id, found := machines[rateKey(m)]
			if !found {
				ok = false
				break
			}
			machineIDs = append(machineIDs, id)
		}
		if !ok {
			result.GespanneSkipped = append(result.GespanneSkipped, r.name)
			continue
		}
		var gid int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO gespanne (base_id, name, tractor_id, load_level_id, sort_order)
			VALUES ($1, $2, $3, $4, $5) RETURNING id`, result.BaseID, r.name, tractorID, loadID, r.sortOrder).Scan(&gid); err != nil {
			return fmt.Errorf("rig %q: %w", r.name, err)
		}
		for _, mid := range machineIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO gespann_machines (gespann_id, machine_id) VALUES ($1, $2)`, gid, mid); err != nil {
				return fmt.Errorf("rig %q machine: %w", r.name, err)
			}
		}
		result.Gespanne++
	}
	return nil
}
