package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// BookingFilter applies the same search to work entries and account postings.
type BookingFilter struct {
	EntryFilter
	Direction string // out = neighbor owes me; in = I owe neighbor
	Kind      string // equipment, labor, quantity, fixed, manual, transfer
}

// BookingRow is a read-only projection. Source disambiguates overlapping IDs;
// only rows sourced from entries may be submitted to booking bulk actions.
type BookingRow struct {
	EntryRow
	Source    string
	Direction string
	Kind      string
	Booking   *models.LedgerBooking
}

// IsLedger distinguishes account postings from outgoing work entries.
func (r BookingRow) IsLedger() bool { return r.Source == "ledger" }

// KindLabel names legacy postings and carry-forwards without calling them work.
func (r BookingRow) KindLabel() string {
	switch r.Kind {
	case "equipment":
		return "Traktor / Geräte"
	case "labor":
		return "Mannstunden"
	case "quantity":
		return "Mengenleistung"
	case "fixed":
		return "Freie Kosten"
	case "transfer":
		return "Jahresübertrag"
	default:
		return "Manuelle Position"
	}
}

// DirectionLabel describes the claim from the operator's perspective.
func (r BookingRow) DirectionLabel() string {
	if r.Direction == "in" {
		return "Ich schulde"
	}
	return "Nachbar schuldet"
}

// bookingRowsSQL retains both sources in one sortable, pageable read model.
// Account amounts are already signed; they never enter the outgoing invoice or
// own-machine usage model merely because the overview shows them together.
const bookingRowsSQL = `WITH bookings AS (
	SELECT e.id, e.billing_year_id, e.neighbor_id, e.entry_date,
	       e.task_label, e.note, e.unit, e.hours, e.quantity, e.unit_price,
	       e.cost, e.voided, e.linked_entry_id,
	       'entry'::text AS source, 'out'::text AS direction,
	       CASE WHEN e.person_id IS NOT NULL OR e.unit = 'Mannstunde' THEN 'labor'
	            WHEN e.unit = 'h' THEN 'equipment' ELSE 'quantity' END AS kind,
	       NULL::jsonb AS booking, ''::text AS detail
	  FROM entries e WHERE e.billing_year_id = $1
	UNION ALL
	SELECT l.id, l.billing_year_id, l.neighbor_id, l.posting_date,
	       COALESCE(l.booking->>'task_label', l.description),
	       COALESCE(l.booking->>'note', ''), COALESCE(l.booking->>'unit', ''),
	       CASE WHEN l.booking->>'unit' = 'h' THEN (l.booking->>'quantity')::numeric ELSE 0 END,
	       COALESCE((l.booking->>'quantity')::numeric, 0),
	       COALESCE((l.booking->>'unit_price')::numeric, 0),
	       l.amount, l.voided, NULL::bigint, 'ledger'::text,
	       CASE WHEN l.amount < 0 THEN 'in' ELSE 'out' END,
	       CASE WHEN COALESCE(l.transfer_id, '') <> '' THEN 'transfer'
	            ELSE COALESCE(l.booking->>'kind', 'manual') END,
	       l.booking, l.description
	  FROM neighbor_ledger l WHERE l.billing_year_id = $1
) `

// bookingFilterWhere shares ordinary entry filters and extends literal search
// to the snapshotted equipment/person description of incoming services.
func bookingFilterWhere(f BookingFilter) (string, []any) {
	base := f.EntryFilter
	base.Task = ""
	where, args := entryFilterWhere(base)
	add := func(column, value string) {
		args = append(args, value)
		where += " AND " + column + " = $" + strconv.Itoa(len(args))
	}
	if task := strings.TrimSpace(f.Task); task != "" {
		args = append(args, likeEscape(strings.ToLower(task)))
		p := "$" + strconv.Itoa(len(args))
		where += " AND (lower(e.task_label) LIKE " + p + " ESCAPE '\\' OR lower(e.note) LIKE " + p +
			" ESCAPE '\\' OR lower(e.detail) LIKE " + p + " ESCAPE '\\')"
	}
	switch f.Direction {
	case "in", "out":
		add("e.direction", f.Direction)
	}
	switch f.Kind {
	case "equipment", "labor", "quantity", "fixed", "manual", "transfer":
		add("e.kind", f.Kind)
	}
	return where, args
}

// bookingFilterOrder includes the source to break ties between entry/ledger IDs.
func bookingFilterOrder(f BookingFilter) string {
	return entryFilterOrder(f.Sort, f.Desc) + ", e.source"
}

// scanBookingRow decodes a projection without pretending a ledger ID is an entry.
func scanBookingRow(rows *sql.Rows) (BookingRow, error) {
	var r BookingRow
	var booking []byte
	err := rows.Scan(&r.ID, &r.NeighborID, &r.Date, &r.TaskLabel, &r.Note,
		&r.Unit, &r.Hours, &r.Quantity, &r.UnitPrice, &r.Cost, &r.Voided,
		&r.LinkedEntryID, &r.NeighborName, &r.Source, &r.Direction, &r.Kind, &booking)
	if err != nil {
		return r, fmt.Errorf("scan unified booking: %w", err)
	}
	if len(booking) != 0 {
		if err := json.Unmarshal(booking, &r.Booking); err != nil {
			return r, fmt.Errorf("decode unified booking detail: %w", err)
		}
	}
	return r, nil
}

const bookingSelectSQL = `SELECT e.id, e.neighbor_id, e.entry_date,
	e.task_label, e.note, e.unit, e.hours, e.quantity, e.unit_price, e.cost,
	e.voided, e.linked_entry_id, n.name, e.source, e.direction, e.kind, e.booking
	FROM bookings e JOIN neighbors n ON n.id = e.neighbor_id`

// FilterBookings returns one combined page and exact signed totals for all
// matches. A read snapshot keeps count and page consistent during concurrent edits.
func (s *Store) FilterBookings(ctx context.Context, f BookingFilter) ([]BookingRow, int, decimal.Decimal, error) {
	where, args := bookingFilterWhere(f)
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, 0, decimal.Zero, fmt.Errorf("begin booking overview: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var total int
	var sum decimal.Decimal
	// #nosec G202 -- all SQL fragments are constant or generated placeholders.
	countSQL := bookingRowsSQL + `SELECT count(*), COALESCE(SUM(CASE WHEN e.voided THEN 0 ELSE e.cost END), 0)
		FROM bookings e` + where
	if err := tx.QueryRowContext(ctx, countSQL, args...).Scan(&total, &sum); err != nil {
		return nil, 0, sum, fmt.Errorf("count unified bookings: %w", err)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset := max(f.Offset, 0)
	args = append(args, limit, offset)
	// #nosec G202 -- only allowlisted ordering and numbered parameters are composed.
	query := bookingRowsSQL + bookingSelectSQL + where + bookingFilterOrder(f) +
		" LIMIT $" + strconv.Itoa(len(args)-1) + " OFFSET $" + strconv.Itoa(len(args))
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, sum, fmt.Errorf("query unified bookings: %w", err)
	}
	defer rows.Close()
	var out []BookingRow
	for rows.Next() {
		r, err := scanBookingRow(rows)
		if err != nil {
			return nil, 0, sum, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, sum, fmt.Errorf("iterate unified bookings: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, 0, sum, fmt.Errorf("close unified bookings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, sum, fmt.Errorf("finish booking overview: %w", err)
	}
	return out, total, sum, nil
}

// ExportBookings returns all matches, ignoring UI pagination so exports never
// silently stop after the first 50 or 500 rows of a billing year.
func (s *Store) ExportBookings(ctx context.Context, f BookingFilter) ([]BookingRow, error) {
	where, args := bookingFilterWhere(f)
	// #nosec G202 -- query fragments are constants, allowlisted order, and placeholders.
	query := bookingRowsSQL + bookingSelectSQL + where + bookingFilterOrder(f)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query booking export: %w", err)
	}
	defer rows.Close()
	var out []BookingRow
	for rows.Next() {
		r, err := scanBookingRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate booking export: %w", err)
	}
	return out, nil
}

// BookingUnitsInYear also exposes units used only on incoming services.
func (s *Store) BookingUnitsInYear(ctx context.Context, yearID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT unit FROM (
		SELECT unit FROM entries WHERE billing_year_id=$1
		UNION SELECT booking->>'unit' FROM neighbor_ledger WHERE billing_year_id=$1
	) units WHERE unit IS NOT NULL AND unit <> '' ORDER BY unit`, yearID)
	if err != nil {
		return nil, fmt.Errorf("query booking units: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var unit string
		if err := rows.Scan(&unit); err != nil {
			return nil, fmt.Errorf("scan booking unit: %w", err)
		}
		out = append(out, unit)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate booking units: %w", err)
	}
	return out, nil
}
