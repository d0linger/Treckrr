package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

var (
	// ErrInvoiceLocked reports that an issued invoice froze the account's
	// booking basis. Callers may still use the explicit settlement operations.
	ErrInvoiceLocked = errors.New("account has an issued invoice")
	// ErrNeighborAnonymized prevents ordinary writers from repopulating personal
	// working data after erasure.
	ErrNeighborAnonymized = errors.New("neighbor is anonymized")
	// ErrIdempotencyConflict means a replay key belongs to another account.
	ErrIdempotencyConflict = errors.New("idempotency key belongs to another account")
)

// The payable balance for one neighbor in one year, as a single statement so it
// can be evaluated inside the transaction that is about to act on it. Once an
// invoice is issued its frozen gross replaces the live booking net. Active credit
// notes and ledger postings then adjust that liability and payments reduce it.
// Before issuance, the live booking net is the deliberate fallback.
const remainingSQL = `
	SELECT COALESCE(
	         (SELECT gross FROM invoices
	           WHERE billing_year_id=$1 AND neighbor_id=$2
	             AND kind='invoice' AND status='issued'),
	         (SELECT COALESCE(SUM(cost),0) FROM entries
	           WHERE billing_year_id=$1 AND neighbor_id=$2 AND NOT voided),
	         0)
	     + (SELECT COALESCE(SUM(gross),0) FROM invoices
	         WHERE billing_year_id=$1 AND neighbor_id=$2
	           AND kind='gutschrift' AND status='issued')
	     + (SELECT COALESCE(SUM(amount),0) FROM neighbor_ledger
	         WHERE billing_year_id=$1 AND neighbor_id=$2 AND NOT voided)
	     - (SELECT COALESCE(SUM(amount),0) FROM payments
	         WHERE billing_year_id=$1 AND neighbor_id=$2 AND deleted_at IS NULL)`

// lockAccount takes a row lock on the membership row for (year, neighbor) and
// reports whether that account exists at all.
//
// Settling and carrying forward both DERIVE what they write from the current
// balance, so reading it outside the writing transaction is a read-then-write
// race: concurrent requests each see the same open amount and each post it.
// Locking the membership row serializes every such operation on one account
// while leaving other accounts untouched.
//
// billing_year_neighbors is the natural mutex here — it has a primary key on
// exactly (billing_year_id, neighbor_id), so the row identifies the account
// precisely. An advisory lock would need that pair packed into an integer key,
// which cannot be done for two int64s without truncation.
func lockAccount(ctx context.Context, tx *sql.Tx, yearID, neighborID int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM billing_year_neighbors
		  WHERE billing_year_id=$1 AND neighbor_id=$2 FOR UPDATE`, yearID, neighborID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // neighbor is not part of that year: nothing to settle or carry
	}
	return err == nil, err
}

// lockAccountBoundary establishes the global lock order for account writers:
// year advisory lock, year row, neighbor row, then membership row. requireOpen
// is false only for explicit settlement operations that remain valid after the
// booking year is completed.
func lockAccountBoundary(
	ctx context.Context,
	tx *sql.Tx,
	yearID, neighborID int64,
	requireOpen bool,
	allowAnonymized bool,
) (bool, error) {
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return false, err
	}
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT status FROM billing_years WHERE id=$1 FOR NO KEY UPDATE`, yearID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if requireOpen && status == "completed" {
		return false, ErrYearCompleted
	}
	var anonymized bool
	err = tx.QueryRowContext(ctx,
		`SELECT anonymized FROM neighbors WHERE id=$1 FOR NO KEY UPDATE`, neighborID).Scan(&anonymized)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if anonymized && !allowAnonymized {
		return false, ErrNeighborAnonymized
	}
	return lockAccount(ctx, tx, yearID, neighborID)
}

func lockMutableBookingAccount(ctx context.Context, tx *sql.Tx, yearID, neighborID int64) error {
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, true, false)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	var invoiced bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM invoices WHERE billing_year_id=$1 AND neighbor_id=$2
		  AND kind='invoice' AND status='issued')`, yearID, neighborID).Scan(&invoiced); err != nil {
		return err
	}
	if invoiced {
		return ErrInvoiceLocked
	}
	return nil
}

func lockOpenAccount(ctx context.Context, tx *sql.Tx, yearID, neighborID int64) error {
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, true, false)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

type accountKey struct {
	yearID, neighborID int64
}

// lockSettlementAccounts locks multiple accounts in deterministic order. It is
// used by carry-forward, whose source and destination must join the same
// serialization boundary as every payment and ledger writer.
func sortedUniqueAccounts(accounts []accountKey) []accountKey {
	sort.Slice(accounts, func(i, j int) bool {
		if accounts[i].yearID == accounts[j].yearID {
			return accounts[i].neighborID < accounts[j].neighborID
		}
		return accounts[i].yearID < accounts[j].yearID
	})
	unique := accounts[:0]
	for _, account := range accounts {
		if len(unique) == 0 || unique[len(unique)-1] != account {
			unique = append(unique, account)
		}
	}
	return unique
}

// lockAccounts acquires every lock class for a set of accounts before moving
// to the next class. That global order prevents two bulk operations covering
// the same accounts in a different input order from deadlocking.
func lockAccounts(
	ctx context.Context,
	tx *sql.Tx,
	accounts []accountKey,
	requireOpen bool,
	allowAnonymized bool,
) error {
	accounts = sortedUniqueAccounts(accounts)
	if len(accounts) == 0 {
		return nil
	}

	var lastYearID int64
	haveLastYear := false
	for _, account := range accounts {
		if haveLastYear && account.yearID == lastYearID {
			continue
		}
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, account.yearID); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRowContext(ctx,
			`SELECT status FROM billing_years WHERE id=$1 FOR NO KEY UPDATE`,
			account.yearID,
		).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if requireOpen && status == "completed" {
			return ErrYearCompleted
		}
		lastYearID = account.yearID
		haveLastYear = true
	}
	neighborIDs := make([]int64, 0, len(accounts))
	seenNeighbors := map[int64]struct{}{}
	for _, account := range accounts {
		if _, seen := seenNeighbors[account.neighborID]; seen {
			continue
		}
		seenNeighbors[account.neighborID] = struct{}{}
		neighborIDs = append(neighborIDs, account.neighborID)
	}
	sort.Slice(neighborIDs, func(i, j int) bool { return neighborIDs[i] < neighborIDs[j] })
	rows, err := tx.QueryContext(ctx, `
		SELECT id, anonymized
		  FROM neighbors
		 WHERE id = ANY($1)
		 ORDER BY id
		 FOR NO KEY UPDATE`, neighborIDs)
	if err != nil {
		return err
	}
	lockedNeighbors := 0
	for rows.Next() {
		var id int64
		var anonymized bool
		if err := rows.Scan(&id, &anonymized); err != nil {
			_ = rows.Close()
			return err
		}
		if anonymized && !allowAnonymized {
			_ = rows.Close()
			return ErrNeighborAnonymized
		}
		lockedNeighbors++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if lockedNeighbors != len(neighborIDs) {
		return ErrNotFound
	}
	for _, account := range accounts {
		ok, err := lockAccount(ctx, tx, account.yearID, account.neighborID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNotFound
		}
	}
	return nil
}

func lockSettlementAccounts(ctx context.Context, tx *sql.Tx, accounts ...accountKey) error {
	return lockAccounts(ctx, tx, accounts, false, true)
}

func lockMutableAccounts(ctx context.Context, tx *sql.Tx, accounts ...accountKey) error {
	accounts = sortedUniqueAccounts(accounts)
	if err := lockAccounts(ctx, tx, accounts, true, false); err != nil {
		return err
	}
	for _, account := range accounts {
		var invoiced bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM invoices WHERE billing_year_id=$1 AND neighbor_id=$2
			  AND kind='invoice' AND status='issued')`, account.yearID, account.neighborID).Scan(&invoiced); err != nil {
			return err
		}
		if invoiced {
			return ErrInvoiceLocked
		}
	}
	return nil
}

func lockOpenAccounts(ctx context.Context, tx *sql.Tx, accounts ...accountKey) error {
	return lockAccounts(ctx, tx, accounts, true, false)
}

func remainingLocked(ctx context.Context, tx *sql.Tx, yearID, neighborID int64) (decimal.Decimal, error) {
	var d decimal.Decimal
	err := tx.QueryRowContext(ctx, remainingSQL, yearID, neighborID).Scan(&d)
	return d, err
}

// AccountRemaining returns the same payable amount used by settle, carry and
// payout actions. Keeping display and mutation paths on one SQL definition
// prevents a VAT invoice from appearing paid while its gross is still open.
func (s *Store) AccountRemaining(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	var d decimal.Decimal
	err := s.db.QueryRowContext(ctx, remainingSQL, yearID, neighborID).Scan(&d)
	return d, err
}

// SettleRemaining books a payment for whatever is still open on the account and
// returns the amount booked; a zero return means there was nothing to settle.
//
// The amount is recomputed under the account lock rather than passed in, so two
// concurrent "Rest als bezahlt" clicks cannot both book the same balance.
func (s *Store) SettleRemaining(ctx context.Context, yearID, neighborID int64, when time.Time, note string) (decimal.Decimal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return decimal.Zero, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, false, true)
	if err != nil || !ok {
		return decimal.Zero, err
	}
	remaining, err := remainingLocked(ctx, tx, yearID, neighborID)
	if err != nil {
		return decimal.Zero, err
	}
	if !remaining.IsPositive() {
		return decimal.Zero, tx.Commit()
	}
	// invoice_id via subselect: 0044 added the attribution column and updated
	// two of the three payment INSERTs — settle-generated payments stayed
	// unlinked and showed a blank Rechnung column in the Zahlungshistorie.
	// method stays '': how the money arrived is genuinely unknown here.
	var paymentID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note, invoice_id)
		 VALUES ($1,$2,$3,$4,$5,
		         (SELECT id FROM invoices WHERE billing_year_id=$1 AND neighbor_id=$2
		           AND kind='invoice' AND status='issued'))
		 RETURNING id`, yearID, neighborID, remaining, when, note).Scan(&paymentID); err != nil {
		return decimal.Zero, err
	}
	if err := addAuditTx(
		ctx,
		tx,
		"payment_settle",
		"payment",
		strconv.FormatInt(paymentID, 10),
		paymentAuditState(remaining, when, ""),
	); err != nil {
		return decimal.Zero, err
	}
	return remaining, tx.Commit()
}

// CarryForwardRemaining moves the open balance of (fromYearID, neighborID) into
// toYearID as a linked pair of ledger postings, and returns the amount moved; a
// zero return means there was nothing to carry.
//
// Like SettleRemaining the amount is recomputed under the lock. Without it the
// operation did not merely duplicate: once enough transfers had landed the
// balance turned NEGATIVE, and a negative balance carries as a reverse posting
// that raises the source year again, so each round fed the next. Measured on the
// dev stack, 24 concurrent requests turned a €200 balance into €10,400 of
// postings.
//
// Both accounts are locked in canonical order. This keeps transfers from racing
// with settlement or invoicing on either side without introducing lock-ordering
// deadlocks.
func (s *Store) CarryForwardRemaining(ctx context.Context, neighborID, fromYearID, toYearID int64, when time.Time, fromDesc, toDesc string) (decimal.Decimal, error) {
	tid, err := randToken()
	if err != nil {
		return decimal.Zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return decimal.Zero, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockSettlementAccounts(
		ctx,
		tx,
		accountKey{yearID: fromYearID, neighborID: neighborID},
		accountKey{yearID: toYearID, neighborID: neighborID},
	); err != nil {
		return decimal.Zero, err
	}
	remaining, err := remainingLocked(ctx, tx, fromYearID, neighborID)
	if err != nil {
		return decimal.Zero, err
	}
	if remaining.IsZero() {
		return decimal.Zero, tx.Commit()
	}
	// Both sides share transfer_id so the pair reverses atomically (see
	// DeleteLedgerTransfer / SetLedgerVoidedTransfer).
	const ins = `INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date, transfer_id)
	             VALUES ($1,$2,$3,$4,$5,$6)`
	if _, err := tx.ExecContext(ctx, ins, fromYearID, neighborID, remaining.Neg(), fromDesc, when, tid); err != nil {
		return decimal.Zero, err
	}
	if _, err := tx.ExecContext(ctx, ins, toYearID, neighborID, remaining, toDesc, when, tid); err != nil {
		return decimal.Zero, err
	}
	if err := addAuditTx(
		ctx,
		tx,
		"carry_forward",
		"ledger_transfer",
		tid,
		fmt.Sprintf("amount=%s; from_year_id=%d; to_year_id=%d", remaining.StringFixed(2), fromYearID, toYearID),
	); err != nil {
		return decimal.Zero, err
	}
	return remaining, tx.Commit()
}

// PayoutCredit books a neighbor's credit balance (a negative open amount) as
// paid out and returns the amount booked; a zero return means there was no
// credit to pay out.
//
// Like SettleRemaining the amount is recomputed under the account lock rather
// than passed in from a read the caller made earlier. Without that, two
// concurrent "Guthaben auszahlen" clicks each see the same credit and each post
// it, paying the neighbor out twice — the same read-then-write race that turned
// a EUR 200 balance into EUR 10,400 of postings before CarryForwardRemaining
// was locked.
func (s *Store) PayoutCredit(ctx context.Context, yearID, neighborID int64, when time.Time, description string) (decimal.Decimal, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return decimal.Zero, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	ok, err := lockAccountBoundary(ctx, tx, yearID, neighborID, false, true)
	if err != nil || !ok {
		return decimal.Zero, err
	}
	remaining, err := remainingLocked(ctx, tx, yearID, neighborID)
	if err != nil {
		return decimal.Zero, err
	}
	if !remaining.IsNegative() {
		return decimal.Zero, tx.Commit()
	}
	amount := remaining.Neg()
	var ledgerID int64
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		yearID, neighborID, amount, description, when).Scan(&ledgerID); err != nil {
		return decimal.Zero, err
	}
	if err := addAuditTx(
		ctx,
		tx,
		"credit_payout",
		"ledger",
		strconv.FormatInt(ledgerID, 10),
		fmt.Sprintf("amount=%s; account_year_id=%d", amount.StringFixed(2), yearID),
	); err != nil {
		return decimal.Zero, err
	}
	return amount, tx.Commit()
}
