package store

import (
	"context"
	"strconv"
	"strings"
)

func neighborPath(id int64) string { return "/neighbors/" + strconv.FormatInt(id, 10) }
func belegPath(neighborID, yearID int64) string {
	return "/neighbors/" + strconv.FormatInt(neighborID, 10) + "/beleg?year=" + strconv.FormatInt(yearID, 10)
}

// SearchResult is one hit for the global search / command palette.
type SearchResult struct {
	Kind  string `json:"kind"`  // neighbor | invoice | gespann
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

// Search returns up to perKind matches for each entity (neighbors, invoices,
// gespanne), combined into a flat, navigable list. Case-insensitive ILIKE — no
// extension needed at this scale.
func (s *Store) Search(ctx context.Context, q string, perKind int) ([]SearchResult, error) {
	pat := likeEscape(q)
	out := make([]SearchResult, 0, perKind*3)

	// Neighbors (name or note).
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, note FROM neighbors
		WHERE name ILIKE $1 ESCAPE '\' OR note ILIKE $1 ESCAPE '\'
		ORDER BY archived, name LIMIT $2`, pat, perKind)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var name, note string
		if err := rows.Scan(&id, &name, &note); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, SearchResult{Kind: "neighbor", Label: name, Sub: note, URL: neighborPath(id)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Invoices by number → link to that neighbor's Beleg for the invoice's year.
	rows, err = s.db.QueryContext(ctx, `
		SELECT iv.number, n.name, iv.neighbor_id, iv.billing_year_id
		FROM invoices iv
		JOIN neighbors n ON n.id = iv.neighbor_id
		WHERE iv.number ILIKE $1 ESCAPE '\'
		ORDER BY iv.issued_on DESC LIMIT $2`, pat, perKind)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var number, name string
		var nid, yid int64
		if err := rows.Scan(&number, &name, &nid, &yid); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, SearchResult{Kind: "invoice", Label: "Rechnung " + number, Sub: name, URL: belegPath(nid, yid)})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Gespanne (fixed tractor+load+machine combos) by name.
	rows, err = s.db.QueryContext(ctx, `
		SELECT name FROM gespanne WHERE name ILIKE $1 ESCAPE '\' ORDER BY name LIMIT $2`, pat, perKind)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, SearchResult{Kind: "gespann", Label: name, Sub: "Gespann", URL: "/gespanne"})
	}
	rows.Close()
	return out, rows.Err()
}
