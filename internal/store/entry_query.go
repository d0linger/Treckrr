package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Buchungsliste mit Filter/Sortierung/Blättern (Ausbaukarte 63) ---------

// EntryFilter narrows the year's bookings. A zero value means "no filter", so
// the empty filter returns the whole year, paged.
type EntryFilter struct {
	YearID     int64
	NeighborID int64     // 0 = all
	From, To   time.Time // zero = open ended
	Task       string    // substring, case-insensitive, matched on label AND note
	Unit       string    // exact unit, "" = all
	Voided     string    // "only" / "hide" / "" = both
	Sort       string    // date | cost | neighbor
	Desc       bool
	Limit      int
	Offset     int
}

// entryFilterWhere builds the shared WHERE clause and its arguments. Kept in
// one place so the page query and the count query can never drift apart —
// which is how paginated lists start lying about their totals.
func entryFilterWhere(f EntryFilter) (string, []any) {
	where := []string{"e.billing_year_id = $1"}
	args := []any{f.YearID}
	add := func(cond string, val any) {
		args = append(args, val)
		where = append(where, strings.Replace(cond, "?", "$"+strconv.Itoa(len(args)), 1))
	}
	if f.NeighborID != 0 {
		add("e.neighbor_id = ?", f.NeighborID)
	}
	if !f.From.IsZero() {
		add("e.entry_date >= ?", f.From)
	}
	if !f.To.IsZero() {
		add("e.entry_date <= ?", f.To)
	}
	if t := strings.TrimSpace(f.Task); t != "" {
		// One placeholder used twice: append the arg once, reference it twice.
		// likeEscape (search.go) both escapes the LIKE wildcards and adds the
		// surrounding %: the typed text is a literal, so searching for "50%" or
		// "a_b" must not quietly match half the year.
		args = append(args, likeEscape(strings.ToLower(t)))
		p := "$" + strconv.Itoa(len(args))
		where = append(where, "(lower(e.task_label) LIKE "+p+" ESCAPE '\\' OR lower(e.note) LIKE "+p+" ESCAPE '\\')")
	}
	if u := strings.TrimSpace(f.Unit); u != "" {
		add("e.unit = ?", u)
	}
	switch f.Voided {
	case "only":
		where = append(where, "e.voided")
	case "hide":
		where = append(where, "NOT e.voided")
	}
	return " WHERE " + strings.Join(where, " AND "), args
}

// entryFilterOrder maps the sort choice to a fixed ORDER BY. Never string
// interpolation of user input: only these three constants can reach SQL.
func entryFilterOrder(sort string, desc bool) string {
	dir := " ASC"
	if desc {
		dir = " DESC"
	}
	switch sort {
	case "cost":
		return " ORDER BY e.cost" + dir + ", e.id" + dir
	case "neighbor":
		return " ORDER BY n.name" + dir + ", e.entry_date" + dir + ", e.id" + dir
	default:
		return " ORDER BY e.entry_date" + dir + ", e.id" + dir
	}
}

// EntryRow is one booking of the list, with its neighbor's name resolved.
type EntryRow struct {
	models.Entry
	NeighborName string
}

// FilterEntries returns one page of bookings plus the total number of matches
// (for the pager) and the summed cost of ALL matches — not just the page, so
// the total under a filter means what it says.
func (s *Store) FilterEntries(ctx context.Context, f EntryFilter) ([]EntryRow, int, decimal.Decimal, error) {
	where, args := entryFilterWhere(f)

	var total int
	var sum decimal.Decimal
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*), COALESCE(SUM(CASE WHEN e.voided THEN 0 ELSE e.cost END), 0)
		   FROM entries e JOIN neighbors n ON n.id = e.neighbor_id`+where,
		args...).Scan(&total, &sum); err != nil {
		return nil, 0, sum, err
	}

	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	args = append(args, limit, f.Offset)
	// Every fragment concatenated here is generated, never user text: `where`
	// holds only "$n" placeholders (entryFilterWhere passes the values as args),
	// entryFilterOrder returns one of three constants, and the LIMIT/OFFSET
	// placeholders are numbers derived from len(args).
	// entryColsE derives from entryCols (entries.go): a new entry column now
	// reaches this query automatically instead of being the third hand-edit.
	// #nosec G202 -- columns and ordering are trusted; filter and pagination values are bound parameters.
	q := `SELECT ` + entryColsE + `, n.name
		FROM entries e JOIN neighbors n ON n.id = e.neighbor_id` + where +
		entryFilterOrder(f.Sort, f.Desc) +
		" LIMIT $" + strconv.Itoa(len(args)-1) + " OFFSET $" + strconv.Itoa(len(args))

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, sum, err
	}
	defer rows.Close()
	var out []EntryRow
	for rows.Next() {
		var r EntryRow
		e, err := scanEntryWithName(rows, &r.NeighborName)
		if err != nil {
			return nil, 0, sum, err
		}
		r.Entry = e
		out = append(out, r)
	}
	return out, total, sum, rows.Err()
}

// EntryUnitsInYear lists the units actually used in a year, for the filter's
// dropdown — offering units nobody booked would be noise.
func (s *Store) EntryUnitsInYear(ctx context.Context, yearID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT unit FROM entries WHERE billing_year_id=$1 AND unit <> '' ORDER BY unit`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---- Sammelaktionen (Ausbaukarte 64) ---------------------------------------

func lockMutableEntryAccounts(ctx context.Context, tx *sql.Tx, ids []int64) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT e.billing_year_id, e.neighbor_id
		  FROM entries e
		  JOIN billing_year_neighbors byn
		    ON byn.billing_year_id = e.billing_year_id AND byn.neighbor_id = e.neighbor_id
		 WHERE e.id = ANY($1)
		 ORDER BY e.billing_year_id, e.neighbor_id`, ids)
	if err != nil {
		return err
	}
	accounts := []accountKey{}
	for rows.Next() {
		var account accountKey
		if err := rows.Scan(&account.yearID, &account.neighborID); err != nil {
			_ = rows.Close()
			return err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Bulk actions deliberately skip locked accounts instead of failing the
	// entire selection. Lock every referenced account first, then let the
	// mutation below filter on the protected year/invoice/privacy state.
	return lockSettlementAccounts(ctx, tx, accounts...)
}

// VoidEntries marks several bookings as canceled in one transaction, skipping
// ids that belong to a closed year or to a neighbor whose invoice is already
// festgeschrieben — the same two locks a single void obeys, enforced here in
// SQL so a crafted id list cannot slip past them. Returns how many were voided.
func (s *Store) VoidEntries(ctx context.Context, ids []int64, void bool, reason string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockMutableEntryAccounts(ctx, tx, ids); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE entries e SET voided = $2, void_reason = CASE WHEN $2 THEN $3 ELSE '' END
		 WHERE e.id = ANY($1)
		   AND e.voided <> $2
		   AND EXISTS (SELECT 1 FROM billing_year_neighbors byn
		                WHERE byn.billing_year_id = e.billing_year_id
		                  AND byn.neighbor_id = e.neighbor_id)
		   AND EXISTS (SELECT 1 FROM billing_years y
		                WHERE y.id = e.billing_year_id AND y.status <> 'completed')
		   AND EXISTS (SELECT 1 FROM neighbors n
		                WHERE n.id = e.neighbor_id AND NOT n.anonymized)
		   AND NOT EXISTS (SELECT 1 FROM invoices iv
		                    WHERE iv.billing_year_id = e.billing_year_id
		                      AND iv.neighbor_id = e.neighbor_id
		                      AND iv.kind = 'invoice' AND iv.status = 'issued')`,
		ids, void, reason)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}

// DeleteEntries removes several bookings under the same two locks. A booking
// is deleted only while its year is open and no invoice froze it.
func (s *Store) DeleteEntries(ctx context.Context, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockMutableEntryAccounts(ctx, tx, ids); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		DELETE FROM entries e
		 WHERE e.id = ANY($1)
		   AND EXISTS (SELECT 1 FROM billing_year_neighbors byn
		                WHERE byn.billing_year_id = e.billing_year_id
		                  AND byn.neighbor_id = e.neighbor_id)
		   AND EXISTS (SELECT 1 FROM billing_years y
		                WHERE y.id = e.billing_year_id AND y.status <> 'completed')
		   AND EXISTS (SELECT 1 FROM neighbors n
		                WHERE n.id = e.neighbor_id AND NOT n.anonymized)
		   AND NOT EXISTS (SELECT 1 FROM invoices iv
		                    WHERE iv.billing_year_id = e.billing_year_id
		                      AND iv.neighbor_id = e.neighbor_id
		                      AND iv.kind = 'invoice' AND iv.status = 'issued')`, ids)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), tx.Commit()
}
