package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

func neighborPath(id int64) string { return "/neighbors/" + strconv.FormatInt(id, 10) }
func belegPath(neighborID, yearID int64) string {
	return "/neighbors/" + strconv.FormatInt(neighborID, 10) + "/beleg?year=" + strconv.FormatInt(yearID, 10)
}

// pricesPath / gespannePath link to a basis's price-list and rig-management pages,
// where its tractors/machines/load levels and gespanne are maintained.
func pricesPath(baseID int64) string   { return "/prices?base=" + strconv.FormatInt(baseID, 10) }
func gespannePath(baseID int64) string { return "/gespanne?base=" + strconv.FormatInt(baseID, 10) }

// SearchResult is one hit for the global search / command palette.
type SearchResult struct {
	Kind  string `json:"kind"`  // neighbor | invoice | base | tractor | machine | load | gespann
	Label string `json:"label"` // primary text
	Sub   string `json:"sub"`   // secondary text
	URL   string `json:"url"`   // where to navigate
}

// likeEscape neutralizes ILIKE wildcards in user input so a term like "50%" is
// searched literally rather than as a pattern.
func likeEscape(q string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(q) + "%"
}

// searchSub runs one entity sub-query (bound to the escaped pattern $1 and the
// per-kind cap $2) and appends a SearchResult per row via mk. Centralizing the
// query/scan/close plumbing keeps each entity a couple of lines and always closes
// rows, even on a scan error.
func (s *Store) searchSub(ctx context.Context, out *[]SearchResult, query, pat string, perKind int, mk func(*sql.Rows) (SearchResult, error)) error {
	rows, err := s.db.QueryContext(ctx, query, pat, perKind)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		res, err := mk(rows)
		if err != nil {
			return err
		}
		*out = append(*out, res)
	}
	return rows.Err()
}

// Search returns up to perKind matches per entity, combined into a flat, navigable
// list. Case-insensitive ILIKE across the maintained master data — neighbors,
// invoices, and each basis with its tractors / machines / load levels / gespanne —
// with numbers searched via ::text, so "948" finds a tractor and "2025" a basis.
// No extension needed at this scale.
func (s *Store) Search(ctx context.Context, q string, perKind int) ([]SearchResult, error) {
	pat := likeEscape(q)
	out := make([]SearchResult, 0, perKind*7)

	// Neighbors (name or note).
	if err := s.searchSub(ctx, &out, `
		SELECT id, name, note FROM neighbors
		WHERE name ILIKE $1 ESCAPE '\' OR note ILIKE $1 ESCAPE '\'
		ORDER BY archived, name LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var id int64
		var name, note string
		if err := rows.Scan(&id, &name, &note); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "neighbor", Label: name, Sub: note, URL: neighborPath(id)}, nil
	}); err != nil {
		return nil, err
	}

	// Invoices by number → link to that neighbor's Beleg for the invoice's year.
	if err := s.searchSub(ctx, &out, `
		SELECT iv.number, n.name, iv.neighbor_id, iv.billing_year_id
		FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		WHERE iv.number ILIKE $1 ESCAPE '\'
		ORDER BY iv.issued_on DESC LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var number, name string
		var nid, yid int64
		if err := rows.Scan(&number, &name, &nid, &yid); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "invoice", Label: "Rechnung " + number, Sub: name, URL: belegPath(nid, yid)}, nil
	}); err != nil {
		return nil, err
	}

	// Price bases (Bemessungsgrundlagen) by name or "valid from" year.
	if err := s.searchSub(ctx, &out, `
		SELECT id, name, year FROM price_bases
		WHERE name ILIKE $1 ESCAPE '\' OR year::text ILIKE $1 ESCAPE '\'
		ORDER BY year DESC LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var id int64
		var name string
		var year int
		if err := rows.Scan(&id, &name, &year); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "base", Label: name, Sub: "Bemessungsgrundlage · " + strconv.Itoa(year), URL: pricesPath(id)}, nil
	}); err != nil {
		return nil, err
	}

	// Tractors by model (ident/name) or horsepower (ps::text, so "948" matches the
	// number too). trim_scale drops a NUMERIC's trailing zeros for a clean "180 PS".
	if err := s.searchSub(ctx, &out, `
		SELECT t.ident, trim_scale(t.ps)::text, t.base_id, b.year
		FROM tractors t JOIN price_bases b ON b.id = t.base_id
		WHERE t.ident ILIKE $1 ESCAPE '\' OR t.name ILIKE $1 ESCAPE '\' OR t.ps::text ILIKE $1 ESCAPE '\'
		ORDER BY b.year DESC, t.ident LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var ident, ps string
		var baseID int64
		var year int
		if err := rows.Scan(&ident, &ps, &baseID, &year); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "tractor", Label: ident, Sub: "Traktor · " + ps + " PS · " + strconv.Itoa(year), URL: pricesPath(baseID)}, nil
	}); err != nil {
		return nil, err
	}

	// Machines by name.
	if err := s.searchSub(ctx, &out, `
		SELECT m.name, m.base_id, b.year FROM machines m JOIN price_bases b ON b.id = m.base_id
		WHERE m.name ILIKE $1 ESCAPE '\'
		ORDER BY b.year DESC, m.name LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var name string
		var baseID int64
		var year int
		if err := rows.Scan(&name, &baseID, &year); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "machine", Label: name, Sub: "Maschine · " + strconv.Itoa(year), URL: pricesPath(baseID)}, nil
	}); err != nil {
		return nil, err
	}

	// Load levels (Belastungsstufen) by name.
	if err := s.searchSub(ctx, &out, `
		SELECT l.name, l.base_id, b.year FROM load_levels l JOIN price_bases b ON b.id = l.base_id
		WHERE l.name ILIKE $1 ESCAPE '\'
		ORDER BY b.year DESC, l.name LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var name string
		var baseID int64
		var year int
		if err := rows.Scan(&name, &baseID, &year); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "load", Label: name, Sub: "Belastungsstufe · " + strconv.Itoa(year), URL: pricesPath(baseID)}, nil
	}); err != nil {
		return nil, err
	}

	// Gespanne (fixed tractor+load+machine combos) by name.
	if err := s.searchSub(ctx, &out, `
		SELECT g.name, g.base_id FROM gespanne g
		WHERE g.name ILIKE $1 ESCAPE '\' ORDER BY g.name LIMIT $2`, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var name string
		var baseID int64
		if err := rows.Scan(&name, &baseID); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "gespann", Label: name, Sub: "Gespann", URL: gespannePath(baseID)}, nil
	}); err != nil {
		return nil, err
	}

	return out, nil
}
