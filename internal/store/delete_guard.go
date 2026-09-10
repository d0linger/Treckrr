package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrHasHistory is returned when deletion would lose retained financial or
// delivery records. Handlers use the same refusal for a precheck, a protected
// transaction, or a RESTRICT foreign-key violation.
var ErrHasHistory = errors.New("record still referenced by financial history")

// isForeignKeyViolation reports whether err is a Postgres referential-integrity
// violation. It inspects the typed driver error rather than searching the message
// text: a substring match would also fire on an unrelated error that happens to
// contain those five digits, and would stop working if the wrapper ever
// reformatted the message. pgconn ships inside the pgx module the driver already
// comes from, so this adds no new dependency.
//
// Two codes, because the one Postgres raises for ON DELETE RESTRICT — which is
// what 0039 puts on the financial tables — depends on the server version:
//
//	                       PG 16      PG 18
//	ON DELETE RESTRICT     23503      23001   (restrict_violation)
//	ON DELETE NO ACTION    23503      23503   (foreign_key_violation)
//
// Verified by executing a delete against both servers. Matching only 23503 works
// today (we ship PG 16) but would silently stop working on an upgrade: the guard
// would fall through, and a blocked delete would surface as an unhandled 500
// instead of ErrHasHistory and the "which records stand in the way" message.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23503" || pgErr.Code == "23001"
}

// DeleteBlockers counts the money- and tax-relevant records that a cascading
// DELETE would destroy along with its parent row.
//
// Entries, payments, ledger, invoices and beleg_sends are protected by RESTRICT
// foreign keys. Dunning history and retained outbox rows are protected by the
// transactional delete guard below. Memberships, share links and recurring
// templates may still cascade when none of this history exists.
type DeleteBlockers struct {
	Entries  int
	Payments int
	Ledger   int
	Invoices int
	// Sends counts beleg_sends rows. It needs its own entry rather than folding
	// into Invoices because the two are not equivalent: handleBelegMarkSent
	// records a send with no invoice check at all, so a "manuell versendet" mark
	// can exist for a neighbor that was never invoiced. 0039 makes beleg_sends
	// RESTRICT, so without counting it the database would refuse a delete the
	// precheck had just declared fine — the unexplained failure this type exists
	// to prevent.
	Sends int
	// These newer tables still have cascading foreign keys. The delete path
	// checks them under a parent row lock, alongside the existing constraints.
	DunningNotices int
	// Include sent mail until normal purge: its history writes follow the sent
	// status update, so excluding it would open a deletion gap between them.
	Outbox int
}

// Any reports whether anything at all would be destroyed.
func (b DeleteBlockers) Any() bool {
	hasMoney := b.Entries > 0 || b.Payments > 0 || b.Ledger > 0 || b.Invoices > 0
	hasDelivery := b.Sends > 0 || b.DunningNotices > 0 || b.Outbox > 0
	return hasMoney || hasDelivery
}

// NeighborDeleteBlockers counts what a DELETE of the neighbor would hit, across
// every billing year. One round trip.
//
// Soft-deleted payments COUNT. They are still rows, so the 0039 RESTRICT
// constraints refuse the delete because of them; excluding them here would make
// the precheck disagree with the database and turn a clear refusal into an
// unexplained "Löschen fehlgeschlagen". They remain durable financial evidence;
// physical deletion requires a separately reviewed retention/legal-hold policy.
func (s *Store) NeighborDeleteBlockers(ctx context.Context, neighborID int64) (DeleteBlockers, error) {
	var b DeleteBlockers
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM entries         WHERE neighbor_id = $1),
		       (SELECT count(*) FROM payments        WHERE neighbor_id = $1),
		       (SELECT count(*) FROM neighbor_ledger WHERE neighbor_id = $1),
		       (SELECT count(*) FROM invoices        WHERE neighbor_id = $1),
		       (SELECT count(*) FROM beleg_sends     WHERE neighbor_id = $1),
		       (SELECT count(*) FROM dunning_notices WHERE neighbor_id = $1),
		       (SELECT count(*) FROM mail_outbox WHERE neighbor_id = $1)`,
		neighborID).Scan(&b.Entries, &b.Payments, &b.Ledger, &b.Invoices, &b.Sends, &b.DunningNotices, &b.Outbox)
	return b, err
}

// YearDeleteBlockers is NeighborDeleteBlockers for a whole billing year.
func (s *Store) YearDeleteBlockers(ctx context.Context, yearID int64) (DeleteBlockers, error) {
	var b DeleteBlockers
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM entries         WHERE billing_year_id = $1),
		       (SELECT count(*) FROM payments        WHERE billing_year_id = $1),
		       (SELECT count(*) FROM neighbor_ledger WHERE billing_year_id = $1),
		       (SELECT count(*) FROM invoices        WHERE billing_year_id = $1),
		       (SELECT count(*) FROM beleg_sends     WHERE billing_year_id = $1),
		       (SELECT count(*) FROM dunning_notices WHERE billing_year_id = $1),
		       (SELECT count(*) FROM mail_outbox WHERE billing_year_id = $1)`,
		yearID).Scan(&b.Entries, &b.Payments, &b.Ledger, &b.Invoices, &b.Sends, &b.DunningNotices, &b.Outbox)
	return b, err
}

// deleteWithDeliveryGuard supplements the financial-history RESTRICT constraints
// for newer delivery tables whose foreign keys still cascade. All query strings
// are internal constants, never user input. Locking the parent first serializes
// this check with child inserts through their foreign-key key-share locks.
func (s *Store) deleteWithDeliveryGuard(ctx context.Context, id int64, lockQuery, historyQuery, deleteQuery string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var lockedID int64
	if err := tx.QueryRowContext(ctx, lockQuery, id).Scan(&lockedID); errors.Is(err, sql.ErrNoRows) {
		return nil // preserve the idempotent delete contract
	} else if err != nil {
		return err
	}
	var hasHistory bool
	if err := tx.QueryRowContext(ctx, historyQuery, id).Scan(&hasHistory); err != nil {
		return err
	}
	if hasHistory {
		return ErrHasHistory
	}
	if _, err := tx.ExecContext(ctx, deleteQuery, id); err != nil {
		if isForeignKeyViolation(err) {
			return ErrHasHistory
		}
		return err
	}
	return tx.Commit()
}
