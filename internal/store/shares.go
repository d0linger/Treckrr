package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// CreateBelegShare stores the HASH of a freshly minted share token (the raw
// token is never persisted) with its owner Beleg and chosen validity.
// Opportunistically prunes dead rows (revoked, or expired for over 30 days) so
// the table never needs external maintenance — the Parkrr portal-link pattern.
func (s *Store) CreateBelegShare(ctx context.Context, tokenHash string, neighborID, yearID int64, expires time.Time, createdBy string) (int64, error) {
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM beleg_shares WHERE revoked OR expires_at < now() - interval '30 days'`)
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO beleg_shares (token_hash, neighbor_id, billing_year_id, expires_at, created_by)
		 VALUES ($1,$2,$3,$4,$5) RETURNING id`,
		tokenHash, neighborID, yearID, expires, createdBy).Scan(&id)
	return id, err
}

// ResolveBelegShare validates a token hash and returns the Beleg it grants.
// A missing, expired or revoked link yields ok=false (the public 404 path).
// last_used_at is updated best-effort — a failure never blocks the view.
func (s *Store) ResolveBelegShare(ctx context.Context, tokenHash string) (neighborID, yearID int64, ok bool, err error) {
	var id int64
	err = s.db.QueryRowContext(ctx,
		`SELECT id, neighbor_id, billing_year_id FROM beleg_shares
		  WHERE token_hash = $1 AND NOT revoked AND expires_at > now()`,
		tokenHash).Scan(&id, &neighborID, &yearID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE beleg_shares SET last_used_at = now() WHERE id = $1`, id)
	return neighborID, yearID, true, nil
}

// ListBelegShares returns a Beleg's active (unrevoked, unexpired) links,
// newest first — the self-service list on the Beleg page.
func (s *Store) ListBelegShares(ctx context.Context, neighborID, yearID int64) ([]models.BelegShare, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, expires_at, created_at, created_by, last_used_at
		   FROM beleg_shares
		  WHERE neighbor_id = $1 AND billing_year_id = $2 AND NOT revoked AND expires_at > now()
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

// RevokeBelegShare revokes one link, scoped to its neighbor so a forged id in
// the form can never touch another neighbor's links. Idempotent; reports
// whether a link was actually revoked.
func (s *Store) RevokeBelegShare(ctx context.Context, id, neighborID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE beleg_shares SET revoked = TRUE WHERE id = $1 AND neighbor_id = $2 AND NOT revoked`,
		id, neighborID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
