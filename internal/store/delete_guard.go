package store

import (
	"context"
	"errors"
	"strings"
)

// ErrHasHistory is returned when the database itself refuses to delete a row
// because financial or tax-relevant records still reference it (the 0039
// RESTRICT constraints). Handlers translate it into the same refusal the
// precheck produces, so the race between the two reads the same to the user.
var ErrHasHistory = errors.New("record still referenced by financial history")

// isForeignKeyViolation reports whether err is Postgres SQLSTATE 23503. It
// matches on the code text rather than importing pgconn for one check, which
// keeps the store free of a driver-specific dependency it otherwise avoids.
func isForeignKeyViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "23503")
}

// DeleteBlockers counts the money- and tax-relevant records that a cascading
// DELETE would destroy along with its parent row.
//
// Both neighbors and billing_years are referenced ON DELETE CASCADE by eight
// tables (entries, billing_year_neighbors, neighbor_ledger, payments, invoices,
// beleg_sends, recurring_entries, beleg_shares). Guarding on bookings alone let a
// neighbor with no bookings but a carry-forward, a payment or an issued invoice
// be deleted, taking that history with it silently — exactly the records § 132 BAO
// requires to be kept for seven years, and the ones the Festschreibung is meant to
// freeze. The four counted here are the ones that represent money or a tax
// document; the rest (memberships, share links, recurring templates, send history)
// carry no history of their own and may cascade.
type DeleteBlockers struct {
	Entries  int
	Payments int
	Ledger   int
	Invoices int
}

// Any reports whether anything at all would be destroyed.
func (b DeleteBlockers) Any() bool {
	return b.Entries > 0 || b.Payments > 0 || b.Ledger > 0 || b.Invoices > 0
}

// NeighborDeleteBlockers counts what a DELETE of the neighbor would hit, across
// every billing year. One round trip.
//
// Soft-deleted payments COUNT. They are still rows, so the 0039 RESTRICT
// constraints refuse the delete because of them; excluding them here would make
// the precheck disagree with the database and turn a clear refusal into an
// unexplained "Löschen fehlgeschlagen". They also remain restorable until the
// undo grace period expires, so blocking is the honest answer — and it is
// self-healing, since PurgeDeletedPayments removes them after seven days.
func (s *Store) NeighborDeleteBlockers(ctx context.Context, neighborID int64) (DeleteBlockers, error) {
	var b DeleteBlockers
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM entries         WHERE neighbor_id = $1),
		       (SELECT count(*) FROM payments        WHERE neighbor_id = $1),
		       (SELECT count(*) FROM neighbor_ledger WHERE neighbor_id = $1),
		       (SELECT count(*) FROM invoices        WHERE neighbor_id = $1)`,
		neighborID).Scan(&b.Entries, &b.Payments, &b.Ledger, &b.Invoices)
	return b, err
}

// YearDeleteBlockers is NeighborDeleteBlockers for a whole billing year.
func (s *Store) YearDeleteBlockers(ctx context.Context, yearID int64) (DeleteBlockers, error) {
	var b DeleteBlockers
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM entries         WHERE billing_year_id = $1),
		       (SELECT count(*) FROM payments        WHERE billing_year_id = $1),
		       (SELECT count(*) FROM neighbor_ledger WHERE billing_year_id = $1),
		       (SELECT count(*) FROM invoices        WHERE billing_year_id = $1)`,
		yearID).Scan(&b.Entries, &b.Payments, &b.Ledger, &b.Invoices)
	return b, err
}
