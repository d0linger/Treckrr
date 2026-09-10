package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// The open balance for one neighbor in one year, as a single statement so it can
// be evaluated inside the transaction that is about to act on it. Mirrors the
// server's neighborRemaining: work bookings plus signed ledger postings minus
// recorded payments, ignoring voided/soft-deleted rows on every side.
const remainingSQL = `
	SELECT (SELECT COALESCE(SUM(cost),0)   FROM entries
	         WHERE billing_year_id=$1 AND neighbor_id=$2 AND NOT voided)
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

func remainingLocked(ctx context.Context, tx *sql.Tx, yearID, neighborID int64) (decimal.Decimal, error) {
	var d decimal.Decimal
	err := tx.QueryRowContext(ctx, remainingSQL, yearID, neighborID).Scan(&d)
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
	ok, err := lockAccount(ctx, tx, yearID, neighborID)
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
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount, paid_on, note, invoice_id)
		 VALUES ($1,$2,$3,$4,$5,
		         (SELECT id FROM invoices WHERE billing_year_id=$1 AND neighbor_id=$2
		           AND kind='invoice' AND status='issued'))`, yearID, neighborID, remaining, when, note); err != nil {
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
// Only the source account is locked. The destination side is a plain insert
// whose amount derives from the source, so there is nothing to serialize there —
// and locking one row rather than two keeps this free of lock-ordering deadlocks
// against a concurrent settle on the other year.
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
	ok, err := lockAccount(ctx, tx, fromYearID, neighborID)
	if err != nil || !ok {
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
	ok, err := lockAccount(ctx, tx, yearID, neighborID)
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
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
		 VALUES ($1,$2,$3,$4,$5)`, yearID, neighborID, amount, description, when); err != nil {
		return decimal.Zero, err
	}
	return amount, tx.Commit()
}
