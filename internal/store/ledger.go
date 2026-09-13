package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ListNeighborLedger returns a neighbor's manual account postings for a year,
// oldest first. Voided postings are included (shown struck-through) but do not
// count toward the balance.
func (s *Store) ListNeighborLedger(ctx context.Context, yearID, neighborID int64) ([]models.LedgerEntry, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, amount, description, posting_date, voided, void_reason, created_at, transfer_id, booking
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
		var booking []byte
		if err := rows.Scan(&e.ID, &e.Amount, &e.Description, &e.Date, &e.Voided, &e.VoidReason, &e.Created, &e.TransferID, &booking); err != nil {
			return nil, err
		}
		if len(booking) != 0 {
			if err := json.Unmarshal(booking, &e.Booking); err != nil {
				return nil, fmt.Errorf("decode ledger booking: %w", err)
			}
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
	Payable    decimal.Decimal // frozen invoice gross plus issued credits, or live booking net before issuance
	Remaining  decimal.Decimal // Payable + Ledger - PaidAmount
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
		               AND p.billing_year_id = byn.billing_year_id AND p.deleted_at IS NULL), 0) AS paid,
		  COALESCE((SELECT gross FROM invoices iv
		             WHERE iv.neighbor_id = byn.neighbor_id
		               AND iv.billing_year_id = byn.billing_year_id
		               AND iv.kind = 'invoice' AND iv.status = 'issued'),
		           (SELECT SUM(e.cost) FROM entries e
		             WHERE e.neighbor_id = byn.neighbor_id
		               AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0)
		  + COALESCE((SELECT SUM(iv.gross) FROM invoices iv
		               WHERE iv.neighbor_id = byn.neighbor_id
		                 AND iv.billing_year_id = byn.billing_year_id
		                 AND iv.kind = 'gutschrift' AND iv.status = 'issued'), 0) AS payable
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
		if err := rows.Scan(&r.YearID, &r.Year, &r.Status, &r.Cost, &r.Hours, &r.Ledger, &r.PaidAmount, &r.Payable); err != nil {
			return nil, err
		}
		r.Net = r.Cost.Add(r.Ledger)
		r.Remaining = r.Payable.Add(r.Ledger).Sub(r.PaidAmount)
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockOpenAccount(ctx, tx, yearID, neighborID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`, yearID, neighborID, amount, description, date).Scan(&id)
	if err != nil {
		return 0, err
	}
	if err := addAuditTx(ctx, tx, "ledger_add", "ledger", strconv.FormatInt(id, 10), ledgerAuditState(amount, date, false)); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UpdateNeighborLedger edits a posting's amount, description and date.
func (s *Store) UpdateNeighborLedger(ctx context.Context, id int64, amount decimal.Decimal, description string, date time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := ledgerAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockOpenAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	before, err := ledgerForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := protectStructuredLedger(ctx, tx, id, yearID, neighborID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE neighbor_ledger SET amount=$1, description=$2, posting_date=$3 WHERE id=$4 AND booking IS NULL`,
		amount, description, date, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	detail := "before{" + ledgerAuditState(before.Amount, before.Date, before.Voided) + "} after{" +
		ledgerAuditState(amount, date, before.Voided) + "}"
	if err := addAuditTx(ctx, tx, "ledger_update", "ledger", strconv.FormatInt(id, 10), detail); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLedgerVoided marks a posting as voided (or restores it).
func (s *Store) SetLedgerVoided(ctx context.Context, id int64, voided bool, reason string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := ledgerAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockOpenAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	before, err := ledgerForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := protectStructuredLedger(ctx, tx, id, yearID, neighborID); err != nil {
		return err
	}
	if before.Voided == voided {
		return tx.Commit()
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE neighbor_ledger SET voided=$1, void_reason=$2 WHERE id=$3`, voided, reason, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	action := "ledger_unvoid"
	if voided {
		action = "ledger_void"
	}
	if err := addAuditTx(ctx, tx, action, "ledger", strconv.FormatInt(id, 10), ledgerAuditState(before.Amount, before.Date, voided)); err != nil {
		return err
	}
	return tx.Commit()
}

type ledgerMutationRow struct {
	Amount decimal.Decimal
	Date   time.Time
	Voided bool
}

func ledgerAccount(ctx context.Context, tx *sql.Tx, id int64) (yearID, neighborID int64, err error) {
	err = tx.QueryRowContext(ctx,
		`SELECT billing_year_id, neighbor_id FROM neighbor_ledger WHERE id=$1`, id).Scan(&yearID, &neighborID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

func ledgerForUpdate(ctx context.Context, tx *sql.Tx, id int64) (ledgerMutationRow, error) {
	var row ledgerMutationRow
	err := tx.QueryRowContext(ctx,
		`SELECT amount, posting_date, voided FROM neighbor_ledger WHERE id=$1 FOR UPDATE`, id).
		Scan(&row.Amount, &row.Date, &row.Voided)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return row, err
}

func ledgerAuditState(amount decimal.Decimal, date time.Time, voided bool) string {
	return fmt.Sprintf("amount=%s; posting_date=%s; voided=%t", amount.StringFixed(2), date.Format("2006-01-02"), voided)
}

// GetLedgerEntry returns a posting with its owning year/neighbor (used to
// authorize, lock-check, prefill an edit form, and audit).
func (s *Store) GetLedgerEntry(ctx context.Context, id int64) (yearID, neighborID int64, e models.LedgerEntry, err error) {
	var booking []byte
	err = s.db.QueryRowContext(ctx,
		`SELECT billing_year_id, neighbor_id, id, amount, description, posting_date, voided, void_reason, created_at, transfer_id, booking
		   FROM neighbor_ledger WHERE id=$1`, id).
		Scan(&yearID, &neighborID, &e.ID, &e.Amount, &e.Description, &e.Date, &e.Voided, &e.VoidReason, &e.Created, &e.TransferID, &booking)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err == nil && len(booking) != 0 {
		err = json.Unmarshal(booking, &e.Booking)
	}
	return
}

// DeleteNeighborLedger removes a posting.
func (s *Store) DeleteNeighborLedger(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	yearID, neighborID, err := ledgerAccount(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := lockOpenAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	before, err := ledgerForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := protectStructuredLedger(ctx, tx, id, yearID, neighborID); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM neighbor_ledger WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if err := addAuditTx(ctx, tx, "ledger_delete", "ledger", strconv.FormatInt(id, 10), ledgerAuditState(before.Amount, before.Date, before.Voided)); err != nil {
		return err
	}
	return tx.Commit()
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	accounts, err := ledgerTransferAccounts(ctx, tx, transferID)
	if err != nil {
		return err
	}
	// Reversing a transfer is an ordinary ledger edit: completed years must be
	// reopened first, matching the handler's transferYearsOpen guard.
	if err := lockOpenAccounts(ctx, tx, accounts...); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM neighbor_ledger WHERE transfer_id=$1`, transferID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if err := addAuditTx(ctx, tx, "ledger_delete", "ledger_transfer", transferID, fmt.Sprintf("postings=%d", n)); err != nil {
		return err
	}
	return tx.Commit()
}

// SetLedgerVoidedTransfer voids (or restores) both sides of a carry-forward. An
// empty transfer_id is rejected — it would otherwise match every ordinary
// (non-transfer) posting.
func (s *Store) SetLedgerVoidedTransfer(ctx context.Context, transferID string, voided bool, reason string) error {
	if transferID == "" {
		return errors.New("empty transfer id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	accounts, err := ledgerTransferAccounts(ctx, tx, transferID)
	if err != nil {
		return err
	}
	// Keep completed years immutable here as well as in transferYearsOpen.
	if err := lockOpenAccounts(ctx, tx, accounts...); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE neighbor_ledger SET voided=$1, void_reason=$2 WHERE transfer_id=$3`, voided, reason, transferID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	action := "ledger_unvoid"
	if voided {
		action = "ledger_void"
	}
	if err := addAuditTx(ctx, tx, action, "ledger_transfer", transferID, fmt.Sprintf("postings=%d; voided=%t", n, voided)); err != nil {
		return err
	}
	return tx.Commit()
}

func ledgerTransferAccounts(ctx context.Context, tx *sql.Tx, transferID string) ([]accountKey, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT DISTINCT billing_year_id, neighbor_id
		  FROM neighbor_ledger
		 WHERE transfer_id=$1
		 ORDER BY billing_year_id, neighbor_id`, transferID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var accounts []accountKey
	for rows.Next() {
		var account accountKey
		if err := rows.Scan(&account.yearID, &account.neighborID); err != nil {
			return nil, err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrNotFound
	}
	return accounts, nil
}
