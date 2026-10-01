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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockPersonalDataNeighbor(ctx, tx, neighborID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`WITH prune AS (
		     DELETE FROM beleg_shares WHERE expires_at < now() - interval '30 days'
		 )
		 INSERT INTO beleg_shares (token_hash, neighbor_id, billing_year_id, expires_at, created_by)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tokenHash, neighborID, yearID, expires, createdBy).Scan(&id)
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
	var usedToday bool
	err = s.db.QueryRowContext(ctx,
		`SELECT id, neighbor_id, billing_year_id,
		        (last_used_at IS NOT NULL AND last_used_at::date = CURRENT_DATE)
		   FROM beleg_shares
		  WHERE token_hash = $1 AND expires_at > now()`,
		tokenHash).Scan(&id, &neighborID, &yearID, &usedToday)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	if !usedToday {
		_, _ = s.db.ExecContext(ctx, `UPDATE beleg_shares SET last_used_at = now() WHERE id = $1`, id) // best-effort
	}
	return id, neighborID, yearID, true, nil
}

// RecordBelegShareEvent appends a data-minimal portal audit event.
func (s *Store) RecordBelegShareEvent(ctx context.Context, shareID, neighborID, yearID int64, event string) error {
	if shareID == 0 { // legacy HMAC link: no backing share row
		return nil
	}
	switch event {
	case "access", "download", "confirm", "dispute":
	default:
		return fmt.Errorf("record beleg share event: unsupported event %q", event)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO beleg_share_events (share_id,neighbor_id,billing_year_id,event)
		VALUES ($1,$2,$3,$4)`, shareID, neighborID, yearID, event)
	return err
}

// CreateBelegFeedback records a confirmation or dispute after revalidating the
// active share, account and immutable invoice hash in one statement.
func (s *Store) CreateBelegFeedback(ctx context.Context, shareID, invoiceID, neighborID, yearID int64, contentHash, status string, linePosition *int, message string) error {
	if status != "confirmed" && status != "disputed" {
		return fmt.Errorf("create beleg feedback: unsupported status %q", status)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var valid bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM beleg_shares s JOIN invoices i
		  ON i.id=$2 AND i.neighbor_id=s.neighbor_id AND i.billing_year_id=s.billing_year_id
		 WHERE s.id=$1 AND s.neighbor_id=$3 AND s.billing_year_id=$4 AND s.expires_at>now()
		   AND i.kind='invoice' AND i.status='issued' AND i.content_hash=$5)`,
		shareID, invoiceID, neighborID, yearID, contentHash).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrNotFound
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
		`SELECT id, expires_at, created_at, created_by, last_used_at
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
		if err := rows.Scan(&sh.ID, &sh.ExpiresAt, &sh.CreatedAt, &sh.CreatedBy, &sh.LastUsedAt); err != nil {
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
