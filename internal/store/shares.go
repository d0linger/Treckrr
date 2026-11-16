package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// CreateBelegShare stores the HASH of a freshly minted share token (the raw
// token is never persisted) with its owner Beleg and chosen validity. Rows
// long past their expiry are pruned in the same round-trip (CTE), so the
// table never needs external maintenance — the Parkrr portal-link pattern.
// Revocation is a hard DELETE (the audit log is the durable record), so
// expiry is the only liveness condition anywhere.
func (s *Store) CreateBelegShare(ctx context.Context, tokenHash string, neighborID, yearID int64, expires time.Time, createdBy string) (int64, error) {
	return s.createInvoiceBelegShare(ctx, tokenHash, neighborID, yearID, 0, expires, createdBy)
}

// CreateInvoiceBelegShare binds the credential permanently to one active
// document. Its account and status are revalidated inside the insert.
func (s *Store) CreateInvoiceBelegShare(ctx context.Context, tokenHash string, neighborID, yearID, invoiceID int64, expires time.Time, createdBy string) (int64, error) {
	if invoiceID <= 0 {
		return 0, ErrNotFound
	}
	return s.createInvoiceBelegShare(ctx, tokenHash, neighborID, yearID, invoiceID, expires, createdBy)
}

func (s *Store) createInvoiceBelegShare(ctx context.Context, tokenHash string, neighborID, yearID, invoiceID int64, expires time.Time, createdBy string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockPersonalDataNeighbor(ctx, tx, neighborID); err != nil {
		return 0, err
	}
	if invoiceID == 0 {
		// Preserve the privacy guard before legacy unique-only resolution. No
		// year/account locks may be acquired after the personal-data lock.
		rows, err := tx.QueryContext(ctx, `SELECT `+invoiceCols+` FROM invoices
			WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued'
			ORDER BY issued_on,id`, yearID, neighborID)
		if err != nil {
			return 0, err
		}
		invoices, readErr := collectInvoiceRows(rows)
		closeErr := rows.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return 0, err
		}
		invoice, err := uniqueInvoice(invoices)
		if err != nil {
			return 0, err
		}
		invoiceID = invoice.ID
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`WITH prune AS (
		     DELETE FROM beleg_shares WHERE expires_at < now() - interval '30 days'
		 )
		 INSERT INTO beleg_shares (token_hash, neighbor_id, billing_year_id, expires_at, created_by, invoice_id)
		 SELECT $1,$2,$3,$4,$5,id FROM invoices
		 WHERE id=$6 AND neighbor_id=$2 AND billing_year_id=$3
		   AND kind='invoice' AND status='issued' AND content_hash <> ''
		 RETURNING id`,
		tokenHash, neighborID, yearID, expires, createdBy, invoiceID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// ResolveBelegShare validates a token hash and returns the Beleg it grants.
// A missing, expired or deleted link yields ok=false (the public 404 path).
// last_used_at is stamped at CALENDAR-DAY granularity (the UI shows no finer):
// the day boundary is decided by the DATABASE (last_used_at::date vs
// CURRENT_DATE, one timezone), not by a rolling Go duration — so a view just
// before midnight and one just after count as two days, and the
// unauthenticated route writes at most once per day per link.
func (s *Store) ResolveBelegShare(ctx context.Context, tokenHash string) (neighborID, yearID int64, ok bool, err error) {
	_, neighborID, yearID, ok, err = s.ResolveBelegShareAccess(ctx, tokenHash)
	return
}

// ResolveBelegShareAccess is ResolveBelegShare plus the internal share id used
// for access/download audit events. The raw bearer token remains unstored.
func (s *Store) ResolveBelegShareAccess(ctx context.Context, tokenHash string) (id, neighborID, yearID int64, ok bool, err error) {
	id, neighborID, yearID, _, ok, err = s.ResolveInvoiceBelegShareAccess(ctx, tokenHash)
	return
}

// ResolveInvoiceBelegShareAccess never follows another invoice after storno,
// reissue or new work. Unbound legacy rows fail closed.
func (s *Store) ResolveInvoiceBelegShareAccess(ctx context.Context, tokenHash string) (id, neighborID, yearID, invoiceID int64, ok bool, err error) {
	var usedToday bool
	err = s.db.QueryRowContext(ctx,
		`SELECT s.id, s.neighbor_id, s.billing_year_id, s.invoice_id,
		        (s.last_used_at IS NOT NULL AND s.last_used_at::date = CURRENT_DATE)
		   FROM beleg_shares s JOIN invoices i ON i.id=s.invoice_id
		  WHERE s.token_hash=$1 AND s.expires_at>now()
		    AND i.neighbor_id=s.neighbor_id AND i.billing_year_id=s.billing_year_id
		    AND i.kind='invoice' AND i.status='issued' AND i.content_hash <> ''`,
		tokenHash).Scan(&id, &neighborID, &yearID, &invoiceID, &usedToday)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, 0, false, err
	}
	if !usedToday {
		_, _ = s.db.ExecContext(ctx, `UPDATE beleg_shares SET last_used_at = now() WHERE id = $1`, id) // best-effort
	}
	return id, neighborID, yearID, invoiceID, true, nil
}

// RecordBelegShareEvent appends a data-minimal portal audit event. Access and
// download events are kept once per link and hour: a valid link is anonymous,
// and every page view or download must not grow the table without bound.
func (s *Store) RecordBelegShareEvent(ctx context.Context, shareID, neighborID, yearID int64, event string) error {
	if shareID == 0 { // legacy HMAC link: no backing share row
		return nil
	}
	switch event {
	case "access", "download", "confirm", "dispute":
	default:
		return fmt.Errorf("record beleg share event: unsupported event %q", event)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if event == "access" || event == "download" {
		// Lock the link first so concurrent views cannot each pass the hourly
		// check; the check then runs with a fresh READ COMMITTED snapshot.
		var lockedID int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM beleg_shares WHERE id=$1 FOR UPDATE`, shareID).Scan(&lockedID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound // revoked since it was resolved
		}
		if err != nil {
			return err
		}
		var recent bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM beleg_share_events
			WHERE neighbor_id=$2 AND billing_year_id=$3 AND share_id=$1 AND event=$4
			  AND created_at > now() - interval '1 hour')`, shareID, neighborID, yearID, event).Scan(&recent); err != nil {
			return err
		}
		if recent {
			return nil
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO beleg_share_events (share_id,neighbor_id,billing_year_id,event)
		VALUES ($1,$2,$3,$4)`, shareID, neighborID, yearID, event); err != nil {
		return err
	}
	return tx.Commit()
}

// CreateBelegFeedback records a confirmation or dispute after revalidating the
// active share, account and immutable invoice hash in one statement. Like a
// statement link, one invoice link may send feedbackPerDay responses a day;
// beyond that it gets ErrFeedbackLimit.
func (s *Store) CreateBelegFeedback(ctx context.Context, shareID, invoiceID, neighborID, yearID int64, contentHash, status string, linePosition *int, message string) error {
	if status != "confirmed" && status != "disputed" {
		return fmt.Errorf("create beleg feedback: unsupported status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var lockedID int64
	err = tx.QueryRowContext(ctx, `
		SELECT s.id FROM beleg_shares s JOIN invoices i
		  ON i.id=$2 AND i.id=s.invoice_id AND i.neighbor_id=s.neighbor_id AND i.billing_year_id=s.billing_year_id
		 WHERE s.id=$1 AND s.neighbor_id=$3 AND s.billing_year_id=$4 AND s.expires_at>now()
		   AND i.kind='invoice' AND i.status='issued' AND i.content_hash=$5
		 FOR UPDATE OF s`,
		shareID, invoiceID, neighborID, yearID, contentHash).Scan(&lockedID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	// Count after acquiring the link lock, with a fresh READ COMMITTED snapshot.
	var recent int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM beleg_feedback
		WHERE neighbor_id=$2 AND billing_year_id=$3 AND share_id=$1 AND created_at > now() - interval '1 day'`,
		lockedID, neighborID, yearID).Scan(&recent); err != nil {
		return err
	}
	if recent >= feedbackPerDay {
		return ErrFeedbackLimit
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO beleg_feedback
		(share_id,invoice_id,neighbor_id,billing_year_id,content_hash,status,line_position,message)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, shareID, invoiceID, neighborID, yearID,
		contentHash, status, linePosition, message); err != nil {
		return err
	}
	event := "confirm"
	if status == "disputed" {
		event = "dispute"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO beleg_share_events
		(share_id,neighbor_id,billing_year_id,event) VALUES ($1,$2,$3,$4)`,
		shareID, neighborID, yearID, event); err != nil {
		return err
	}
	return tx.Commit()
}

// ListBelegFeedback returns all immutable-snapshot responses newest first.
func (s *Store) ListBelegFeedback(ctx context.Context, neighborID, yearID int64) ([]models.BelegFeedback, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,invoice_id,neighbor_id,billing_year_id,content_hash,
		status,line_position,message,created_at FROM beleg_feedback
		WHERE neighbor_id=$1 AND billing_year_id=$2 ORDER BY created_at DESC,id DESC`, neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BelegFeedback
	for rows.Next() {
		var f models.BelegFeedback
		if err := rows.Scan(&f.ID, &f.InvoiceID, &f.NeighborID, &f.BillingYearID, &f.ContentHash,
			&f.Status, &f.LinePosition, &f.Message, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListBelegShares returns a Beleg's active (unexpired) links, newest first —
// the self-service list on the Beleg page.
func (s *Store) ListBelegShares(ctx context.Context, neighborID, yearID int64) ([]models.BelegShare, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, expires_at, created_at, created_by, last_used_at, invoice_id
		   FROM beleg_shares
		  WHERE neighbor_id = $1 AND billing_year_id = $2 AND expires_at > now()
		  ORDER BY created_at DESC`, neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.BelegShare
	for rows.Next() {
		var sh models.BelegShare
		if err := rows.Scan(&sh.ID, &sh.ExpiresAt, &sh.CreatedAt, &sh.CreatedBy, &sh.LastUsedAt, &sh.InvoiceID); err != nil {
			return nil, err
		}
		sh.NeighborID, sh.BillingYearID = neighborID, yearID
		out = append(out, sh)
	}
	return out, rows.Err()
}

// RevokeBelegShare deletes one link, scoped to the neighbor AND billing year
// the caller is acting on, so a forged id in the form can never touch another
// neighbor's — or another year's — links. A hard DELETE: the link is dead
// instantly, the audit log keeps the history. Idempotent; reports whether a
// link was actually deleted.
func (s *Store) RevokeBelegShare(ctx context.Context, id, neighborID, yearID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM beleg_shares WHERE id = $1 AND neighbor_id = $2 AND billing_year_id = $3`,
		id, neighborID, yearID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) RevokeInvoiceBelegShare(ctx context.Context, id, neighborID, yearID, invoiceID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM beleg_shares
		WHERE id=$1 AND neighbor_id=$2 AND billing_year_id=$3 AND invoice_id=$4`,
		id, neighborID, yearID, invoiceID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
