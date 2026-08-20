package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"unicode/utf8"
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

// ftsWhere builds the fuzzy, diacritic-folded, German-stemmed predicate over the SQL
// text expression expr. expr is trusted (a constant from this file, never user
// input), so string-building it is safe; the user query is bound to $1/$2.
//
//	$1 = raw query    → FTS tsquery (word stemming) + trigram word-similarity (typos)
//	$2 = ILIKE '%q%'  → diacritic-folded substring / model-number match
//
// The trigram term is added only for queries long enough to trigram meaningfully
// (>= 4 runes), so a short numeric query like "948" can't drag in loose fuzzy hits.
// $1::text / $2::text: unaccent is overloaded (text vs regdictionary), so an un-cast
// placeholder is an ambiguous parameter type (42P18).
func ftsWhere(expr string, fuzzy bool) string {
	w := "to_tsvector('german', unaccent(" + expr + ")) @@ websearch_to_tsquery('german', unaccent($1::text))" +
		" OR unaccent(" + expr + ") ILIKE unaccent($2::text) ESCAPE '\\'"
	if fuzzy {
		w += " OR word_similarity(unaccent($1::text), unaccent(" + expr + ")) >= 0.4"
	}
	return "(" + w + ")"
}

// rankOrder is the ORDER BY prefix that ranks a hit by relevance: an exact/substring
// match first, then by trigram closeness. Without it, a loose fuzzy match could crowd
// the real hit out from under the per-kind LIMIT (results are otherwise ordered by
// name/date, not relevance). Placed before each entity's own tiebreak.
func rankOrder(expr string) string {
	return "(unaccent(" + expr + ") ILIKE unaccent($2::text) ESCAPE '\\') DESC," +
		" word_similarity(unaccent($1::text), unaccent(" + expr + ")) DESC"
}

// searchSub runs one entity sub-query (bound to $1 = raw query, $2 = ILIKE pattern,
// $3 = per-kind cap) and appends a SearchResult per row via mk. Centralizing the
// query/scan/close plumbing keeps each entity a couple of lines and always closes
// rows, even on a scan error.
func (s *Store) searchSub(ctx context.Context, out *[]SearchResult, query, rawQ, pat string, perKind int, mk func(*sql.Rows) (SearchResult, error)) error {
	rows, err := s.db.QueryContext(ctx, query, rawQ, pat, perKind)
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
// list, across the maintained master data — neighbors, invoices, and each basis with
// its tractors / machines / load levels / gespanne. Name/text fields match on German
// stemming (to_tsvector), diacritic-folded substring (unaccent + ILIKE) and trigram
// typo tolerance, so "Traktoren" finds "Traktor", "Muller" finds "Müller" and "948"
// finds a tractor by its model number; matches are ranked exact-before-fuzzy.
func (s *Store) Search(ctx context.Context, q string, perKind int) ([]SearchResult, error) {
	// Trim once so the fuzzy gate, the ILIKE pattern ($2) and the tsquery/trigram term
	// ($1) all operate on the same string, regardless of surrounding whitespace a
	// caller may pass (the HTTP handler pre-trims, but Search shouldn't rely on that).
	q = strings.TrimSpace(q)
	pat := likeEscape(q)
	// Only run the trigram word-similarity term for queries long enough for it to be
	// meaningful; below ~one trigram it is noise, and short numeric codes are already
	// covered by the substring match.
	fuzzy := utf8.RuneCountInString(q) >= 4
	out := make([]SearchResult, 0, perKind*7)

	// Neighbors (name or note).
	nExpr := "coalesce(name,'') || ' ' || coalesce(note,'')"
	if err := s.searchSub(ctx, &out, `
		SELECT id, name, note FROM neighbors
		WHERE `+ftsWhere(nExpr, fuzzy)+`
		ORDER BY archived, `+rankOrder(nExpr)+`, name LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
		var id int64
		var name, note string
		if err := rows.Scan(&id, &name, &note); err != nil {
			return SearchResult{}, err
		}
		return SearchResult{Kind: "neighbor", Label: name, Sub: note, URL: neighborPath(id)}, nil
	}); err != nil {
		return nil, err
	}

	// Invoices by number. Same hybrid predicate for consistency (and so $1 is bound
	// here too); the substring term carries the real number matching.
	if err := s.searchSub(ctx, &out, `
		SELECT iv.number, n.name, iv.neighbor_id, iv.billing_year_id
		FROM invoices iv JOIN neighbors n ON n.id = iv.neighbor_id
		WHERE `+ftsWhere("iv.number", fuzzy)+`
		ORDER BY `+rankOrder("iv.number")+`, iv.issued_on DESC LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
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
		WHERE `+ftsWhere("name", fuzzy)+` OR year::text ILIKE $2 ESCAPE '\'
		ORDER BY `+rankOrder("name")+`, year DESC LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
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
	// active DESC first ranks deactivated (retired) tractors below active ones, as the
	// neighbor query ranks archived below active.
	tExpr := "t.ident || ' ' || coalesce(t.name,'')"
	if err := s.searchSub(ctx, &out, `
		SELECT t.ident, trim_scale(t.ps)::text, t.base_id, b.year
		FROM tractors t JOIN price_bases b ON b.id = t.base_id
		WHERE `+ftsWhere(tExpr, fuzzy)+` OR t.ps::text ILIKE $2 ESCAPE '\'
		ORDER BY t.active DESC, `+rankOrder(tExpr)+`, b.year DESC, t.ident LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
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

	// Machines by name (active DESC ranks deactivated machines last, as for tractors).
	if err := s.searchSub(ctx, &out, `
		SELECT m.name, m.base_id, b.year FROM machines m JOIN price_bases b ON b.id = m.base_id
		WHERE `+ftsWhere("m.name", fuzzy)+`
		ORDER BY m.active DESC, `+rankOrder("m.name")+`, b.year DESC, m.name LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
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
		WHERE `+ftsWhere("l.name", fuzzy)+`
		ORDER BY `+rankOrder("l.name")+`, b.year DESC, l.name LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
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
		WHERE `+ftsWhere("g.name", fuzzy)+`
		ORDER BY `+rankOrder("g.name")+`, g.name LIMIT $3`, q, pat, perKind, func(rows *sql.Rows) (SearchResult, error) {
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
