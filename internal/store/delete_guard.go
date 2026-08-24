package store

import "context"

// DeleteBlockers counts the money- and tax-relevant records that a cascading
// DELETE would destroy along with its parent row.
//
// Both neighbors and billing_years are referenced ON DELETE CASCADE by eight
// tables (entries, billing_year_neighbors, neighbor_ledger, payments, invoices,
// beleg_sends, recurring_entries, beleg_shares). Guarding on bookings alone let a
// neighbour with no bookings but a carry-forward, a payment or an issued invoice
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

// NeighborDeleteBlockers counts what a DELETE of the neighbour would cascade into,
// across every billing year. Soft-deleted payments are excluded: the operator has
// already removed those, so they should not block. One round trip.
func (s *Store) NeighborDeleteBlockers(ctx context.Context, neighborID int64) (DeleteBlockers, error) {
	var b DeleteBlockers
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM entries         WHERE neighbor_id = $1),
		       (SELECT count(*) FROM payments        WHERE neighbor_id = $1 AND deleted_at IS NULL),
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
		       (SELECT count(*) FROM payments        WHERE billing_year_id = $1 AND deleted_at IS NULL),
		       (SELECT count(*) FROM neighbor_ledger WHERE billing_year_id = $1),
		       (SELECT count(*) FROM invoices        WHERE billing_year_id = $1)`,
		yearID).Scan(&b.Entries, &b.Payments, &b.Ledger, &b.Invoices)
	return b, err
}
