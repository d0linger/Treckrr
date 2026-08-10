package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// ListNeighborLedger returns a neighbor's manual account postings for a year,
// oldest first. Voided postings are included (shown struck-through) but do not
// count toward the balance.
func (s *Store) ListNeighborLedger(ctx context.Context, yearID, neighborID int64) ([]models.LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount, description, posting_date, voided, void_reason, created_at, transfer_id
		   FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND neighbor_id=$2
		  ORDER BY posting_date, id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.LedgerEntry
	for rows.Next() {
		var e models.LedgerEntry
		if err := rows.Scan(&e.ID, &e.Amount, &e.Description, &e.Date, &e.Voided, &e.VoidReason, &e.Created, &e.TransferID); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NeighborLedgerSum returns the signed sum of a neighbor's non-voided ledger
// for a year (positive = extra receivable, negative = payable).
func (s *Store) NeighborLedgerSum(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(amount),0) FROM neighbor_ledger
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND NOT voided`, yearID, neighborID).Scan(&sum)
	return sum, err
}

// YearNeighborResult is a per-neighbor breakdown for a year: work bookings
// (Leistungen), the signed ledger sum (Verrechnung) and their net.
type YearNeighborResult struct {
	Name       string
	Leistungen decimal.Decimal
	Ledger     decimal.Decimal
	Net        decimal.Decimal
}

// YearNeighborResults returns the per-neighbor Leistungen/Verrechnung/Netto for
// a year (non-voided only), ordered by name — in one query, no per-row fan-out.
func (s *Store) YearNeighborResults(ctx context.Context, yearID int64) ([]YearNeighborResult, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.name,
		  COALESCE((SELECT SUM(e.cost) FROM entries e
		             WHERE e.neighbor_id = n.id
		               AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0) AS leistungen,
		  COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		             WHERE l.neighbor_id = n.id
		               AND l.billing_year_id = byn.billing_year_id
		               AND NOT l.voided), 0) AS ledger
		FROM billing_year_neighbors byn
		JOIN neighbors n ON n.id = byn.neighbor_id
		WHERE byn.billing_year_id = $1
		ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YearNeighborResult
	for rows.Next() {
		var r YearNeighborResult
		if err := rows.Scan(&r.Name, &r.Leistungen, &r.Ledger); err != nil {
			return nil, err
		}
		r.Net = r.Leistungen.Add(r.Ledger)
		out = append(out, r)
	}
	return out, rows.Err()
}

// NeighborYearHistoryRow is one year of a neighbor's cross-year history:
// bookings (Leistungen), signed ledger sum (Verrechnung), their Net, hours,
// the payment flag and the year's status.
type NeighborYearHistoryRow struct {
	YearID     int64
	Year       int
	Status     string
	Cost       decimal.Decimal // work bookings, not voided
	Ledger     decimal.Decimal // signed manual postings, not voided
	Net        decimal.Decimal // Cost + Ledger
	Hours      decimal.Decimal
	PaidAmount decimal.Decimal // sum of recorded payments
	Remaining  decimal.Decimal // Net − PaidAmount
	Paid       bool            // fully settled (Remaining <= 0)
}

// NeighborYearHistory returns a neighbor's per-year history (newest first) in a
// single query — membership, totals, ledger and paid flag together, replacing a
// 4-queries-per-year fan-out. Years the neighbor is not a member of are
// naturally absent via the billing_year_neighbors join.
func (s *Store) NeighborYearHistory(ctx context.Context, neighborID int64) ([]NeighborYearHistoryRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT y.id, y.year, y.status,
		  COALESCE((SELECT SUM(e.cost) FROM entries e
		             WHERE e.neighbor_id = byn.neighbor_id
		               AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0) AS cost,
		  COALESCE((SELECT SUM(e.hours) FROM entries e
		             WHERE e.neighbor_id = byn.neighbor_id
		               AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0) AS hours,
		  COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		             WHERE l.neighbor_id = byn.neighbor_id
		               AND l.billing_year_id = byn.billing_year_id
		               AND NOT l.voided), 0) AS ledger,
		  COALESCE((SELECT SUM(p.amount) FROM payments p
		             WHERE p.neighbor_id = byn.neighbor_id
		               AND p.billing_year_id = byn.billing_year_id), 0) AS paid
		FROM billing_year_neighbors byn
		JOIN billing_years y ON y.id = byn.billing_year_id
		WHERE byn.neighbor_id = $1
		ORDER BY y.year DESC`, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NeighborYearHistoryRow
	for rows.Next() {
		var r NeighborYearHistoryRow
		if err := rows.Scan(&r.YearID, &r.Year, &r.Status, &r.Cost, &r.Hours, &r.Ledger, &r.PaidAmount); err != nil {
			return nil, err
		}
		r.Net = r.Cost.Add(r.Ledger)
		r.Remaining = r.Net.Sub(r.PaidAmount)
		r.Paid = !r.Remaining.IsPositive()
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountLedgerForNeighborYear returns how many ledger postings a neighbor has in
// a year. Voided postings count too — they are kept as visible history and would
// equally be orphaned by removing the membership.
func (s *Store) CountLedgerForNeighborYear(ctx context.Context, yearID, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM neighbor_ledger WHERE billing_year_id=$1 AND neighbor_id=$2`,
		yearID, neighborID).Scan(&n)
	return n, err
}

// YearTotal is one year's aggregate, oldest first — feeds the tile sparklines
// and the all-years stats table.
type YearTotal struct {
	YearID int64
	Year   int
	Cost   decimal.Decimal
	Hours  decimal.Decimal
	Ledger decimal.Decimal
}

// YearlyTotals returns per-year bookings/hours/ledger across all years (oldest
// first) in one query, for the mini trend sparklines.
func (s *Store) YearlyTotals(ctx context.Context) ([]YearTotal, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT y.id, y.year,
		  COALESCE((SELECT SUM(e.cost)   FROM entries e        WHERE e.billing_year_id = y.id AND NOT e.voided), 0),
		  COALESCE((SELECT SUM(e.hours)  FROM entries e        WHERE e.billing_year_id = y.id AND NOT e.voided), 0),
		  COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l WHERE l.billing_year_id = y.id AND NOT l.voided), 0)
		FROM billing_years y
		ORDER BY y.year`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YearTotal
	for rows.Next() {
		var t YearTotal
		if err := rows.Scan(&t.YearID, &t.Year, &t.Cost, &t.Hours, &t.Ledger); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddNeighborLedger records a manual posting and returns its id.
func (s *Store) AddNeighborLedger(ctx context.Context, yearID, neighborID int64, amount decimal.Decimal, description string, date time.Time) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`, yearID, neighborID, amount, description, date).Scan(&id)
	return id, err
}

// UpdateNeighborLedger edits a posting's amount, description and date.
func (s *Store) UpdateNeighborLedger(ctx context.Context, id int64, amount decimal.Decimal, description string, date time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbor_ledger SET amount=$1, description=$2, posting_date=$3 WHERE id=$4`,
		amount, description, date, id)
	return err
}

// SetLedgerVoided marks a posting as voided (or restores it).
func (s *Store) SetLedgerVoided(ctx context.Context, id int64, voided bool, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbor_ledger SET voided=$1, void_reason=$2 WHERE id=$3`, voided, reason, id)
	return err
}

// GetLedgerEntry returns a posting with its owning year/neighbor (used to
// authorize, lock-check, prefill an edit form, and audit).
func (s *Store) GetLedgerEntry(ctx context.Context, id int64) (yearID, neighborID int64, e models.LedgerEntry, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT billing_year_id, neighbor_id, id, amount, description, posting_date, voided, void_reason, created_at, transfer_id
		   FROM neighbor_ledger WHERE id=$1`, id).
		Scan(&yearID, &neighborID, &e.ID, &e.Amount, &e.Description, &e.Date, &e.Voided, &e.VoidReason, &e.Created, &e.TransferID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// DeleteNeighborLedger removes a posting.
func (s *Store) DeleteNeighborLedger(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM neighbor_ledger WHERE id=$1`, id)
	return err
}

// LedgerTransferYearIDs returns the distinct billing years a transfer touches
// (normally the source and target of a carry-forward), so callers can verify
// none of them is completed before undoing the transfer.
func (s *Store) LedgerTransferYearIDs(ctx context.Context, transferID string) ([]int64, error) {
	if transferID == "" {
		return nil, errors.New("empty transfer id")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT billing_year_id FROM neighbor_ledger WHERE transfer_id=$1`, transferID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteLedgerTransfer removes both sides of a carry-forward (all postings that
// share the transfer_id), so a transfer is undone as a unit and the balance
// reopens in the source year instead of vanishing. An empty transfer_id is
// rejected — it would otherwise match every ordinary (non-transfer) posting.
func (s *Store) DeleteLedgerTransfer(ctx context.Context, transferID string) error {
	if transferID == "" {
		return errors.New("empty transfer id")
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM neighbor_ledger WHERE transfer_id=$1`, transferID)
	return err
}

// SetLedgerVoidedTransfer voids (or restores) both sides of a carry-forward. An
// empty transfer_id is rejected — it would otherwise match every ordinary
// (non-transfer) posting.
func (s *Store) SetLedgerVoidedTransfer(ctx context.Context, transferID string, voided bool, reason string) error {
	if transferID == "" {
		return errors.New("empty transfer id")
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbor_ledger SET voided=$1, void_reason=$2 WHERE transfer_id=$3`, voided, reason, transferID)
	return err
}
