package store

import (
	"context"
	"database/sql"
	"errors"
)

// BookingRef identifies one booking without confusing equal IDs across sources.
type BookingRef struct {
	Source string
	ID     int64
}

// bookingRefIDs validates and deduplicates selections before account locking.
func bookingRefIDs(refs []BookingRef) ([]int64, []int64, error) {
	entries, ledger := []int64{}, []int64{}
	seen := map[BookingRef]bool{}
	for _, ref := range refs {
		if ref.ID <= 0 || (ref.Source != "entry" && ref.Source != "ledger") {
			return nil, nil, errors.New("invalid booking reference")
		}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		if ref.Source == "entry" {
			entries = append(entries, ref.ID)
		} else {
			ledger = append(ledger, ref.ID)
		}
	}
	return entries, ledger, nil
}

// lockBulkBookingAccounts acquires one canonical boundary for both sources.
func lockBulkBookingAccounts(ctx context.Context, tx *sql.Tx, entryIDs, ledgerIDs []int64) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT billing_year_id,neighbor_id FROM (
		SELECT billing_year_id,neighbor_id FROM entries WHERE id=ANY($1)
		UNION ALL SELECT billing_year_id,neighbor_id FROM neighbor_ledger WHERE id=ANY($2)
	) accounts`, entryIDs, ledgerIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	accounts := []accountKey{}
	for rows.Next() {
		var account accountKey
		if err := rows.Scan(&account.yearID, &account.neighborID); err != nil {
			return err
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return lockSettlementAccounts(ctx, tx, accounts...)
}

// VoidBookings reversibly changes a mixed selection, skipping frozen accounts
// and carry transfers. Both sources commit together without deleting history.
func (s *Store) VoidBookings(ctx context.Context, refs []BookingRef, voided bool, reason string) (int, error) {
	entryIDs, ledgerIDs, err := bookingRefIDs(refs)
	if err != nil || len(refs) == 0 {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockBulkBookingAccounts(ctx, tx, entryIDs, ledgerIDs); err != nil {
		return 0, err
	}
	const entryQuery = `UPDATE entries b SET voided=$2,void_reason=CASE WHEN $2 THEN $3 ELSE '' END
		 WHERE b.id=ANY($1) AND b.voided<>$2
		 AND EXISTS(SELECT 1 FROM billing_year_neighbors m WHERE m.billing_year_id=b.billing_year_id AND m.neighbor_id=b.neighbor_id)
		 AND EXISTS(SELECT 1 FROM billing_years y WHERE y.id=b.billing_year_id AND y.status<>'completed')
		 AND EXISTS(SELECT 1 FROM neighbors n WHERE n.id=b.neighbor_id AND NOT n.anonymized)
		 AND NOT EXISTS(SELECT 1 FROM invoices i WHERE i.billing_year_id=b.billing_year_id
		 AND i.neighbor_id=b.neighbor_id AND i.kind='invoice' AND i.status='issued')`
	const ledgerQuery = `UPDATE neighbor_ledger b SET voided=$2,void_reason=CASE WHEN $2 THEN $3 ELSE '' END
		 WHERE b.id=ANY($1) AND b.voided<>$2
		 AND EXISTS(SELECT 1 FROM billing_year_neighbors m WHERE m.billing_year_id=b.billing_year_id AND m.neighbor_id=b.neighbor_id)
		 AND EXISTS(SELECT 1 FROM billing_years y WHERE y.id=b.billing_year_id AND y.status<>'completed')
		 AND EXISTS(SELECT 1 FROM neighbors n WHERE n.id=b.neighbor_id AND NOT n.anonymized)
		 AND NOT EXISTS(SELECT 1 FROM invoices i WHERE i.billing_year_id=b.billing_year_id
		 AND i.neighbor_id=b.neighbor_id AND i.kind='invoice' AND i.status='issued')
		 AND b.transfer_id=''`
	targets := []struct {
		query string
		ids   []int64
	}{
		{query: entryQuery, ids: entryIDs},
		{query: ledgerQuery, ids: ledgerIDs},
	}
	var total int64
	for _, target := range targets {
		if len(target.ids) == 0 {
			continue
		}
		res, err := tx.ExecContext(ctx, target.query, target.ids, voided, reason)
		if err != nil {
			return 0, err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, err
		}
		total += n
	}
	return int(total), tx.Commit()
}
