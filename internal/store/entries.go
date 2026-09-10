package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Neighbors ----------------------------------------------------------

// ListNeighbors returns all neighbors (active first, then archived).
func (s *Store) ListNeighbors(ctx context.Context) ([]models.Neighbor, error) {
	rows, err := s.db.QueryContext(ctx,
		// email and payment_term_days included: the manage page's edit form renders
		// both, and a SELECT without them meant the form showed empty values — a
		// save would then have ERASED the stored e-mail address.
		`SELECT id, name, note, address, tax_id, email, iban, payment_term_days, archived, anonymized, created_at FROM neighbors ORDER BY archived, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Neighbor
	for rows.Next() {
		var n models.Neighbor
		if err := rows.Scan(&n.ID, &n.Name, &n.Note, &n.Address, &n.TaxID, &n.Email, &n.IBAN, &n.PaymentTermDays, &n.Archived, &n.Anonymized, &n.Created); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNeighbor returns a neighbor by id.
func (s *Store) GetNeighbor(ctx context.Context, id int64) (*models.Neighbor, error) {
	var n models.Neighbor
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, note, address, tax_id, email, iban, payment_term_days, archived, anonymized, created_at FROM neighbors WHERE id=$1`, id).
		Scan(&n.ID, &n.Name, &n.Note, &n.Address, &n.TaxID, &n.Email, &n.IBAN, &n.PaymentTermDays, &n.Archived, &n.Anonymized, &n.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &n, err
}

// AnonymizeNeighbor erases the live personal data of a neighbor (DSGVO Art. 17)
// while keeping the row and its bookings/invoices for the legal retention period.
// The name is replaced with a stable non-identifying placeholder (kept unique for
// the UNIQUE(name) constraint), and the neighbor is archived. No-op if already
// anonymized.
//
// Since Ausbaukarte 87 it reaches beyond the master record, because the operator
// types free text all over the app and any of it can name a person: booking notes
// and task labels, ledger descriptions, payment notes, and the receipt photos —
// a Wiegeschein shows names and plates. All of that is live working data with no
// retention claim of its own once the person is erased.
//
// What stays, deliberately: the FROZEN invoice snapshots and the amounts. They
// are the tax record (§ 132 BAO, seven years), and scrubbing the live row while
// the snapshot keeps the same text would be theater rather than erasure.
//
// Everything runs in ONE transaction: a half-anonymised person is worse than
// none, because the operator would believe the erasure happened.
func (s *Store) AnonymizeNeighbor(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	res, err := tx.ExecContext(ctx,
		`UPDATE neighbors
		    SET name = 'anonymisiert #' || id,
		        note = '', address = '', tax_id = '', email = '', iban = '',
		        archived = TRUE, anonymized = TRUE
		  WHERE id = $1 AND NOT anonymized`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		for _, q := range []string{
			`DELETE FROM entry_photos WHERE entry_id IN (SELECT id FROM entries WHERE neighbor_id = $1)`,
			`UPDATE entries SET note = '', task_label = '' WHERE neighbor_id = $1 AND (note <> '' OR task_label <> '')`,
			`UPDATE neighbor_ledger SET description = '' WHERE neighbor_id = $1 AND description <> ''`,
			`UPDATE payments SET note = '' WHERE neighbor_id = $1 AND note <> ''`,
			`DELETE FROM mail_outbox WHERE neighbor_id = $1`,
			`DELETE FROM beleg_shares WHERE neighbor_id = $1`,
			// Ratenplan notes are operator-typed free text about the person
			// ("zahlt monatlich, Sohn holt das Geld") — the same class as the
			// booking notes above.
			`UPDATE payment_plans SET note = '' WHERE neighbor_id = $1 AND note <> ''`,
			// Recurring rules carry the person's task/note frozen in their
			// template AND would keep materializing new bookings for an erased
			// person — delete them outright, not just their text.
			`DELETE FROM recurring_entries WHERE neighbor_id = $1`,
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	{
		// Either the neighbor is gone or was already anonymized; distinguish so the
		// handler can 404 vs. treat it as a no-op.
		var exists bool
		if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM neighbors WHERE id=$1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// SetNeighborArchived archives or reactivates a neighbor.
func (s *Store) SetNeighborArchived(ctx context.Context, id int64, archived bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE neighbors SET archived=$1 WHERE id=$2`, archived, id)
	return err
}

// SimilarEntryExists reports whether a non-voided booking with the same named
// task already exists for this neighbor+year on the given date — a strong
// duplicate signal used to warn (not block) before a second identical entry. An
// empty task never matches (too weak a signal to warn on).
func (s *Store) SimilarEntryExists(ctx context.Context, neighborID, yearID int64, date time.Time, task string) (bool, error) {
	if strings.TrimSpace(task) == "" {
		return false, nil
	}
	var exists bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM entries
		   WHERE neighbor_id=$1 AND billing_year_id=$2 AND NOT voided
		     AND entry_date=$3 AND task_label=$4)`,
		neighborID, yearID, date, task).Scan(&exists)
	return exists, err
}

// CreateNeighbor inserts a neighbor.
func (s *Store) CreateNeighbor(ctx context.Context, name, note string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO neighbors (name, note) VALUES ($1,$2) RETURNING id`, name, note).Scan(&id)
	return id, err
}

// UpdateNeighbor updates a neighbor.
func (s *Store) UpdateNeighbor(ctx context.Context, id int64, name, note, address, taxID, email, iban string, paymentTermDays *int) error {
	// Never re-populate personal fields on an anonymized neighbor (DSGVO Art. 17):
	// the UI hides the edit form, and this WHERE clause is the server-side backstop
	// against a crafted POST reviving erased data.
	_, err := s.db.ExecContext(ctx,
		`UPDATE neighbors SET name=$1, note=$2, address=$3, tax_id=$4, email=$5, iban=$8, payment_term_days=$7 WHERE id=$6 AND NOT anonymized`,
		name, note, address, taxID, email, id, paymentTermDays, iban)
	return err
}

// DeleteNeighbor removes a neighbor without retained financial or delivery history.
func (s *Store) DeleteNeighbor(ctx context.Context, id int64) error {
	return s.deleteWithDeliveryGuard(
		ctx,
		id,
		`SELECT id FROM neighbors WHERE id=$1 FOR UPDATE`,
		`SELECT EXISTS(SELECT 1 FROM dunning_notices WHERE neighbor_id=$1)
		     OR EXISTS(SELECT 1 FROM mail_outbox WHERE neighbor_id=$1)`,
		`DELETE FROM neighbors WHERE id=$1`,
	)
}

// CountYearsForNeighbor returns how many billing years a neighbor is part of.
func (s *Store) CountYearsForNeighbor(ctx context.Context, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM billing_year_neighbors WHERE neighbor_id=$1`, neighborID).Scan(&n)
	return n, err
}

// AnyNeighbors reports whether at least one neighbor exists (onboarding state).
func (s *Store) AnyNeighbors(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM neighbors)`).Scan(&exists)
	return exists, err
}

// CountEntriesForNeighbor returns the total entries a neighbor has (all years).
func (s *Store) CountEntriesForNeighbor(ctx context.Context, neighborID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM entries WHERE neighbor_id=$1`, neighborID).Scan(&n)
	return n, err
}

// AnyEntries reports whether any booking exists at all (across every year), so
// the first-run onboarding checklist doesn't reappear in a fresh, empty year.
func (s *Store) AnyEntries(ctx context.Context) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM entries)`).Scan(&exists)
	return exists, err
}

// ---- Entries -------------------------------------------------------------

// CreateEntry inserts a booked work entry and links its machines.
// ensureUnit fills the unit fields for an hour booking, so a caller that only
// set Hours/HourlyRate still stores a consistent unit='h' row (quantity = hours,
// unit price = hourly rate) rather than an empty unit / zero quantity.
func ensureUnit(e *models.Entry) {
	if e.Unit == "" {
		e.Unit = "h"
		e.Quantity = e.Hours
		e.UnitPrice = e.HourlyRate
	}
}

// insertEntryTx writes one entry inside the caller's transaction. Returns 0
// (and no error) when the entry's idempotency key already exists — a replayed
// offline booking is a safe no-op, not a failure.
func insertEntryTx(ctx context.Context, tx *sql.Tx, e *models.Entry, machineIDs []int64) (int64, error) {
	ensureUnit(e)
	var id int64
	err := tx.QueryRowContext(ctx,
		`INSERT INTO entries
		   (neighbor_id, billing_year_id, entry_date, task_label, gespann_id, tractor_id, load_level_id,
		    tractor_label, load_label, machine_labels, hours, hourly_rate, cost, note,
		    unit, quantity, unit_price, idempotency_key, person_id, linked_entry_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		 ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL DO NOTHING
		 RETURNING id`,
		e.NeighborID, e.BillingYearID, e.Date, e.TaskLabel, nullInt(e.GespannID), nullInt(e.TractorID),
		nullInt(e.LoadLevelID), e.TractorLabel, e.LoadLabel, e.MachineLabels, e.Hours,
		e.HourlyRate, e.Cost, e.Note, e.Unit, e.Quantity, e.UnitPrice, nullStr(e.IdempotencyKey),
		nullInt(e.PersonID), nullInt(e.LinkedEntryID)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, mid := range machineIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entry_machines (entry_id,machine_id) VALUES ($1,$2)`, id, mid); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (s *Store) CreateEntry(ctx context.Context, e *models.Entry, machineIDs []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	id, err := insertEntryTx(ctx, tx, e, machineIDs)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// CreateEntryPair books a machine entry and its companion (the helper's
// Mannstunden booked alongside the Gespann) in ONE transaction, linking the
// companion to the machine entry. One transaction, because a pair where only
// one half exists misreports the work either as unmanned or as hours without a
// machine — and the operator was told "gespeichert" for both.
//
// Idempotent per half via each entry's own key: on a replay the machine entry's
// insert no-ops, its id is looked up by key so the companion still links to the
// right row, and the companion's own key makes its insert a no-op too. Returns
// (0, 0, nil) when both halves were already recorded.
// If only the machine was deleted, its FK was set to NULL on the surviving
// companion; restoring the machine also restores that link, not its pricing.
func (s *Store) CreateEntryPair(ctx context.Context, e *models.Entry, machineIDs []int64, companion *models.Entry) (mainID, companionID int64, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()
	mainID, err = insertEntryTx(ctx, tx, e, machineIDs)
	if err != nil {
		return 0, 0, err
	}
	linkID := mainID
	if linkID == 0 && e.IdempotencyKey != "" {
		// Replay: the machine entry already exists — link against the stored row.
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM entries WHERE idempotency_key=$1`, e.IdempotencyKey).Scan(&linkID); err != nil {
			return 0, 0, err
		}
	}
	if linkID != 0 {
		companion.LinkedEntryID = &linkID
	}
	companionID, err = insertEntryTx(ctx, tx, companion, nil)
	if err != nil {
		return 0, 0, err
	}
	if mainID != 0 && companionID == 0 && companion.IdempotencyKey != "" {
		// Only repair alongside a newly restored machine, so an ordinary replay
		// stays a no-op. Never steal a helper from an existing pair or change its
		// captured values. It still counts as an existing row (companionID == 0).
		if _, err := tx.ExecContext(ctx, `
			UPDATE entries SET linked_entry_id=$1
			WHERE idempotency_key=$2 AND linked_entry_id IS NULL
			  AND neighbor_id=$3 AND billing_year_id=$4 AND unit='Mannstunde'`,
			mainID, companion.IdempotencyKey, e.NeighborID, e.BillingYearID); err != nil {
			return 0, 0, err
		}
	}
	return mainID, companionID, tx.Commit()
}

// LinkedPartnerID returns the id of the entry paired with this one — the
// machine entry a companion points at, or the companion pointing at this
// machine entry. 0 when the entry is unpaired.
func (s *Store) LinkedPartnerID(ctx context.Context, id int64) (int64, error) {
	var partner sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(
		  (SELECT linked_entry_id FROM entries WHERE id = $1),
		  (SELECT id FROM entries WHERE linked_entry_id = $1 LIMIT 1))`, id).Scan(&partner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !partner.Valid {
		return 0, nil
	}
	return partner.Int64, nil
}

// DeleteEntryPair removes a linked pair in one transaction: deleting only half
// would misreport the work as unmanned or as hours without a machine, and the
// operator confirmed both.
func (s *Store) DeleteEntryPair(ctx context.Context, id, partnerID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// Lock/delete in ascending ID order, matching recurring template creation
	// regardless of which half's delete button was used. The link is ON DELETE
	// SET NULL, so removing the machine first is safe.
	if id > partnerID {
		id, partnerID = partnerID, id
	}
	for _, eid := range []int64{id, partnerID} {
		if _, err := tx.ExecContext(ctx, `DELETE FROM entries WHERE id=$1`, eid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SyncPairHours mirrors an edited booking's hours onto its linked partner: the
// machine entry (unit 'h') gets hours/quantity + cost at its frozen hourly
// rate, the Mannstunden companion gets quantity + cost at its person rate. One
// statement handles both directions via the unit.
func (s *Store) SyncPairHours(ctx context.Context, partnerID int64, hours decimal.Decimal) (decimal.Decimal, error) {
	var cost decimal.Decimal
	err := s.db.QueryRowContext(ctx, `
		UPDATE entries SET
		  hours    = CASE WHEN unit = 'h' THEN $2::numeric ELSE hours END,
		  quantity = $2::numeric,
		  cost     = round($2::numeric * CASE WHEN unit = 'h' THEN hourly_rate ELSE unit_price END, 2)
		WHERE id = $1
		RETURNING cost`, partnerID, hours).Scan(&cost)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, ErrNotFound
	}
	return cost, err
}

// DeleteEntry removes an entry.
func (s *Store) DeleteEntry(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM entries WHERE id=$1`, id)
	return err
}

// EntryMachineIDs returns the machine ids linked to an entry (for edit prefill).
func (s *Store) EntryMachineIDs(ctx context.Context, entryID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT machine_id FROM entry_machines WHERE entry_id=$1 AND machine_id IS NOT NULL`, entryID)
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

// EntryMachineIDsByNeighborYear returns the machine ids of a neighbor's
// non-voided bookings in a year, grouped by entry id — a single query so the
// beleg's Kostengrundlage doesn't fan out one lookup per booking.
func (s *Store) EntryMachineIDsByNeighborYear(ctx context.Context, neighborID, yearID int64) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT em.entry_id, em.machine_id
		  FROM entry_machines em
		  JOIN entries e ON e.id = em.entry_id
		 WHERE e.neighbor_id = $1 AND e.billing_year_id = $2
		   AND NOT e.voided AND em.machine_id IS NOT NULL`, neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var eid, mid int64
		if err := rows.Scan(&eid, &mid); err != nil {
			return nil, err
		}
		out[eid] = append(out[eid], mid)
	}
	return out, rows.Err()
}

// GetEntry returns an entry by id.
func (s *Store) GetEntry(ctx context.Context, id int64) (*models.Entry, error) {
	row := s.db.QueryRowContext(ctx, entrySelect+` WHERE id=$1`, id)
	e, err := scanEntry(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// ListEntries returns entries for a neighbor within a billing year.
func (s *Store) ListEntries(ctx context.Context, neighborID, yearID int64) ([]models.Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		entrySelect+` WHERE neighbor_id=$1 AND billing_year_id=$2 ORDER BY entry_date, id`,
		neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEntries(rows)
}

// ListEntriesByYear returns all entries within a billing year for export.
func (s *Store) ListEntriesByYear(ctx context.Context, yearID int64) ([]models.Entry, error) {
	rows, err := s.db.QueryContext(ctx,
		entrySelect+` WHERE billing_year_id=$1 ORDER BY neighbor_id, entry_date, id`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectEntries(rows)
}

// NeighborTotal returns the summed cost and hours for a neighbor in a year,
// excluding voided (canceled) entries.
func (s *Store) NeighborTotal(ctx context.Context, neighborID, yearID int64) (cost, hours decimal.Decimal, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(cost),0), COALESCE(SUM(hours),0)
		   FROM entries WHERE neighbor_id=$1 AND billing_year_id=$2 AND NOT voided`, neighborID, yearID).
		Scan(&cost, &hours)
	return
}

// YearPaymentTotals returns the received (paid) and outstanding (open) totals for
// a billing year in a single query: paid = the sum of recorded payments, open =
// the sum of each neighbor's remaining balance (net − payments). Replaces a
// per-neighbor fan-out of NeighborTotal calls.
func (s *Store) YearPaymentTotals(ctx context.Context, yearID int64) (paid, open, credit decimal.Decimal, err error) {
	// Per neighbor: net = work bookings + signed ledger postings; paid = recorded
	// payments. Aggregate each side in scalar subqueries first so joining can't
	// multiply rows. "open" clamps each neighbor's remainder at 0 (GREATEST) so a
	// credit does not silently cancel another neighbor's genuine debt; the netted
	// credit is returned separately as "credit".
	err = s.db.QueryRowContext(ctx,
		`WITH per_neighbor AS (`+perNeighborNetPaid+`
		  WHERE byn.billing_year_id = $1
		)
		SELECT COALESCE(SUM(paid), 0),
		       COALESCE(SUM(GREATEST(net - paid, 0)), 0),
		       COALESCE(SUM(GREATEST(paid - net, 0)), 0)
		FROM per_neighbor`, yearID).Scan(&paid, &open, &credit)
	return
}

// perNeighborNetPaid yields one row per (billing year, neighbor) with the net
// amount owed and the amount actually paid. Shared verbatim by the single-year
// and all-years roll-ups so the two can never drift apart — it is a compile-time
// constant, never built from input.
const perNeighborNetPaid = `
		  SELECT byn.billing_year_id AS year_id,
		    COALESCE((SELECT SUM(e.cost) FROM entries e
		               WHERE e.neighbor_id = byn.neighbor_id
		                 AND e.billing_year_id = byn.billing_year_id
		                 AND NOT e.voided), 0)
		    + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		                 WHERE l.neighbor_id = byn.neighbor_id
		                   AND l.billing_year_id = byn.billing_year_id
		                   AND NOT l.voided), 0) AS net,
		    COALESCE((SELECT SUM(p.amount) FROM payments p
		               WHERE p.neighbor_id = byn.neighbor_id
		                 AND p.billing_year_id = byn.billing_year_id AND p.deleted_at IS NULL), 0) AS paid
		  FROM billing_year_neighbors byn`

// YearPaymentTotal is one billing year's payment roll-up.
type YearPaymentTotal struct {
	YearID             int64
	Paid, Open, Credit decimal.Decimal
}

// AllYearPaymentTotals returns the payment roll-up for EVERY billing year in one
// round trip, keyed by year id. The all-years statistics page needs one per year;
// calling YearPaymentTotals in a loop made that page's cost grow with the number
// of billing years, which is the same N+1 YearlyTotals already removed for the
// bookings/ledger side. Years with no members simply have no entry in the map —
// the zero value is the correct answer for them.
func (s *Store) AllYearPaymentTotals(ctx context.Context) (map[int64]YearPaymentTotal, error) {
	rows, err := s.db.QueryContext(ctx,
		`WITH per_neighbor AS (`+perNeighborNetPaid+`
		)
		SELECT year_id,
		       COALESCE(SUM(paid), 0),
		       COALESCE(SUM(GREATEST(net - paid, 0)), 0),
		       COALESCE(SUM(GREATEST(paid - net, 0)), 0)
		FROM per_neighbor
		GROUP BY year_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int64]YearPaymentTotal)
	for rows.Next() {
		var t YearPaymentTotal
		if err := rows.Scan(&t.YearID, &t.Paid, &t.Open, &t.Credit); err != nil {
			return nil, err
		}
		out[t.YearID] = t
	}
	return out, rows.Err()
}

// YearNeighborSummary is one dashboard row for a neighbor in a billing year:
// the net owed (non-voided bookings + signed ledger), hours, the entry count
// (voided included, matching CountEntriesForNeighborYear) and the payment flag.
type YearNeighborSummary struct {
	NeighborID int64
	Name       string
	Cost       decimal.Decimal // net owed (bookings + signed ledger)
	Hours      decimal.Decimal
	Entries    int
	PaidAmount decimal.Decimal // sum of recorded payments
	Remaining  decimal.Decimal // Cost − PaidAmount
	Paid       bool            // fully settled (Remaining <= 0)
}

// YearNeighborSummaries returns one row per neighbor in the year in a single
// query, replacing the dashboard's per-neighbor NeighborTotal +
// CountEntriesForNeighborYear + NeighborLedgerSum fan-out (2+3N round-trips).
// Aggregates ride in scalar subqueries so joins can't multiply rows. Ordered by
// name to match the dashboard list.
func (s *Store) YearNeighborSummaries(ctx context.Context, yearID int64) ([]YearNeighborSummary, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT n.id, n.name,
		  COALESCE((SELECT SUM(e.cost) FROM entries e
		             WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0)
		  + COALESCE((SELECT SUM(l.amount) FROM neighbor_ledger l
		               WHERE l.neighbor_id = n.id AND l.billing_year_id = byn.billing_year_id
		                 AND NOT l.voided), 0) AS net,
		  COALESCE((SELECT SUM(e.hours) FROM entries e
		             WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id
		               AND NOT e.voided), 0) AS hours,
		  (SELECT count(*) FROM entries e
		             WHERE e.neighbor_id = n.id AND e.billing_year_id = byn.billing_year_id) AS entries,
		  COALESCE((SELECT SUM(p.amount) FROM payments p
		             WHERE p.neighbor_id = n.id AND p.billing_year_id = byn.billing_year_id AND p.deleted_at IS NULL), 0) AS paid
		FROM billing_year_neighbors byn
		JOIN neighbors n ON n.id = byn.neighbor_id
		WHERE byn.billing_year_id = $1
		ORDER BY n.name`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []YearNeighborSummary
	for rows.Next() {
		var r YearNeighborSummary
		if err := rows.Scan(&r.NeighborID, &r.Name, &r.Cost, &r.Hours, &r.Entries, &r.PaidAmount); err != nil {
			return nil, err
		}
		r.Remaining = r.Cost.Sub(r.PaidAmount)
		r.Paid = !r.Remaining.IsPositive() // fully settled when nothing remains
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateEntry replaces the editable fields (and pricing snapshot) of an entry
// and its machine links.
func (s *Store) UpdateEntry(ctx context.Context, e *models.Entry, machineIDs []int64) error {
	ensureUnit(e)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE entries SET entry_date=$1, task_label=$2, gespann_id=$3, tractor_id=$4,
			load_level_id=$5, tractor_label=$6, load_label=$7, machine_labels=$8,
			hours=$9, hourly_rate=$10, cost=$11, note=$12,
			unit=$13, quantity=$14, unit_price=$15 WHERE id=$16`,
		e.Date, e.TaskLabel, nullInt(e.GespannID), nullInt(e.TractorID), nullInt(e.LoadLevelID),
		e.TractorLabel, e.LoadLabel, e.MachineLabels, e.Hours, e.HourlyRate, e.Cost, e.Note,
		e.Unit, e.Quantity, e.UnitPrice, e.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM entry_machines WHERE entry_id=$1`, e.ID); err != nil {
		return err
	}
	for _, mid := range machineIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO entry_machines (entry_id,machine_id) VALUES ($1,$2)`, e.ID, mid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetEntryVoided cancels or restores an entry (kept for traceability).
func (s *Store) SetEntryVoided(ctx context.Context, id int64, voided bool, reason string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE entries SET voided=$1, void_reason=$2 WHERE id=$3`, voided, reason, id)
	return err
}

// entryCols is THE entry column list — scanEntryInto knows its order, and
// FilterEntries derives its e.-prefixed twin from it (entryColsE), so a new
// column cannot silently miss one of the query sites again (person_id and
// linked_entry_id each had to be added in three places).
const entryCols = `id, neighbor_id, billing_year_id, entry_date, task_label, gespann_id,
	tractor_id, load_level_id, tractor_label, load_label, machine_labels,
	hours, hourly_rate, cost, note, voided, void_reason, created_at,
	unit, quantity, unit_price, person_id, linked_entry_id`

const entrySelect = `SELECT ` + entryCols + ` FROM entries`

// entryColsE is entryCols with every column e.-prefixed, for queries that join
// (unqualified id/name would be ambiguous there).
var entryColsE = func() string {
	parts := strings.Split(entryCols, ",")
	for i := range parts {
		parts[i] = "e." + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}()

func collectEntries(rows *sql.Rows) ([]models.Entry, error) {
	var out []models.Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// scanEntryWithName reads an entry row that carries the neighbor's name as its
// last column (the filtered list joins it in). It shares scanEntry's column
// order so the two can only drift together.
func scanEntryWithName(sc scanner, name *string) (models.Entry, error) {
	return scanEntryInto(sc, name)
}

func scanEntry(sc scanner) (models.Entry, error) {
	return scanEntryInto(sc, nil)
}

// scanEntryInto is the one place that knows entrySelect's column order. With a
// non-nil name it additionally reads the joined neighbor name.
func scanEntryInto(sc scanner, name *string) (models.Entry, error) {
	var (
		e       models.Entry
		gespann sql.NullInt64
		tractor sql.NullInt64
		load    sql.NullInt64
		person  sql.NullInt64
		linked  sql.NullInt64
		date    time.Time
	)
	dest := []any{&e.ID, &e.NeighborID, &e.BillingYearID, &date, &e.TaskLabel, &gespann,
		&tractor, &load, &e.TractorLabel, &e.LoadLabel, &e.MachineLabels,
		&e.Hours, &e.HourlyRate, &e.Cost, &e.Note, &e.Voided, &e.VoidReason, &e.Created,
		&e.Unit, &e.Quantity, &e.UnitPrice, &person, &linked}
	if name != nil {
		dest = append(dest, name)
	}
	if err := sc.Scan(dest...); err != nil {
		return e, err
	}
	if person.Valid {
		e.PersonID = &person.Int64
	}
	if linked.Valid {
		e.LinkedEntryID = &linked.Int64
	}
	e.Date = date
	if gespann.Valid {
		e.GespannID = &gespann.Int64
	}
	if tractor.Valid {
		e.TractorID = &tractor.Int64
	}
	if load.Valid {
		e.LoadLevelID = &load.Int64
	}
	return e, nil
}
