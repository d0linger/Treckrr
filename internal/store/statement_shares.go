package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrFeedbackLimit refuses further statement feedback once a link has sent its
// daily allowance, so a leaked link cannot flood the operator's account page.
var ErrFeedbackLimit = errors.New("statement feedback limit reached")

// statementFeedbackPerDay bounds the responses one link may send in 24 hours.
const statementFeedbackPerDay = 20

// StatementShare is one revocable public link to a neighbor's running annual
// statement. Only the hash of its bearer token is stored.
type StatementShare struct {
	ID         int64
	ExpiresAt  time.Time
	CreatedBy  string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// StatementFeedback is a neighbor's confirmation or objection. LineLabel and
// LineAmount freeze the open line it referred to; both are empty for feedback
// on the whole statement.
type StatementFeedback struct {
	ID         int64
	Status     string
	LineLabel  string
	LineAmount decimal.NullDecimal
	Message    string
	CreatedAt  time.Time
}

// CreateStatementShare stores the hash of a new statement link. It refuses an
// anonymized neighbor or one outside the billing year (ErrNotFound) and prunes
// links that expired more than 30 days ago in the same statement.
func (s *Store) CreateStatementShare(ctx context.Context, tokenHash string, neighborID, yearID int64, expires time.Time, createdBy string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
		WITH pruned AS (DELETE FROM statement_shares WHERE expires_at < now() - interval '30 days')
		INSERT INTO statement_shares (token_hash, neighbor_id, billing_year_id, expires_at, created_by)
		SELECT $1, $2, $3, $4, $5
		 WHERE EXISTS (SELECT 1 FROM billing_year_neighbors WHERE billing_year_id = $3 AND neighbor_id = $2)
		   AND EXISTS (SELECT 1 FROM neighbors WHERE id = $2 AND NOT anonymized)
		RETURNING id`, tokenHash, neighborID, yearID, expires, createdBy).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// ResolveStatementShare validates a token hash and returns the account it
// grants. Missing, expired and revoked links yield ok=false. last_used_at is
// stamped at most once per calendar day, decided by the database clock.
func (s *Store) ResolveStatementShare(ctx context.Context, tokenHash string) (id, neighborID, yearID int64, ok bool, err error) {
	var usedToday bool
	err = s.db.QueryRowContext(ctx, `
		SELECT sh.id, sh.neighbor_id, sh.billing_year_id,
		       (sh.last_used_at IS NOT NULL AND sh.last_used_at::date = CURRENT_DATE)
		  FROM statement_shares sh JOIN neighbors n ON n.id = sh.neighbor_id
		 WHERE sh.token_hash = $1 AND sh.expires_at > now() AND NOT n.anonymized`,
		tokenHash).Scan(&id, &neighborID, &yearID, &usedToday)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, 0, false, err
	}
	if !usedToday {
		_, _ = s.db.ExecContext(ctx, `UPDATE statement_shares SET last_used_at = now() WHERE id = $1`, id) // best-effort
	}
	return id, neighborID, yearID, true, nil
}

// ListStatementShares returns an account's unexpired links, newest first.
func (s *Store) ListStatementShares(ctx context.Context, neighborID, yearID int64) ([]StatementShare, error) {
	return s.listStatementShares(ctx, neighborID, yearID, true)
}

func (s *Store) listStatementShares(ctx context.Context, neighborID, yearID int64, activeOnly bool) ([]StatementShare, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, expires_at, created_by, created_at, last_used_at
		  FROM statement_shares
		 WHERE neighbor_id = $1 AND billing_year_id = $2 AND (NOT $3 OR expires_at > now())
		 ORDER BY created_at DESC, id DESC`, neighborID, yearID, activeOnly)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatementShare{}
	for rows.Next() {
		var sh StatementShare
		if err := rows.Scan(&sh.ID, &sh.ExpiresAt, &sh.CreatedBy, &sh.CreatedAt, &sh.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// ListAllStatementShares returns every stored link of an account, expired ones
// included, for the data-subject export.
func (s *Store) ListAllStatementShares(ctx context.Context, neighborID, yearID int64) ([]StatementShare, error) {
	return s.listStatementShares(ctx, neighborID, yearID, false)
}

// RevokeStatementShare deletes one link scoped to the account the caller acts
// on, so a forged id cannot touch another neighbor's or year's link.
func (s *Store) RevokeStatementShare(ctx context.Context, shareID, neighborID, yearID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM statement_shares WHERE id = $1 AND neighbor_id = $2 AND billing_year_id = $3`,
		shareID, neighborID, yearID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// CreateStatementFeedback records a response through a still-active link of
// the same account. It never changes bookings or balances. A revoked or expired
// link yields ErrNotFound, an exhausted daily allowance ErrFeedbackLimit.
func (s *Store) CreateStatementFeedback(ctx context.Context, shareID, neighborID, yearID int64, status, lineLabel string, lineAmount decimal.NullDecimal, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var recent int
	err = tx.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM statement_feedback WHERE share_id = sh.id AND created_at > now() - interval '1 day')
		  FROM statement_shares sh
		 WHERE sh.id = $1 AND sh.neighbor_id = $2 AND sh.billing_year_id = $3 AND sh.expires_at > now()
		 FOR UPDATE OF sh`, shareID, neighborID, yearID).Scan(&recent)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if recent >= statementFeedbackPerDay {
		return ErrFeedbackLimit
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO statement_feedback (share_id, neighbor_id, billing_year_id, status, line_label, line_amount, message)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		shareID, neighborID, yearID, status, lineLabel, lineAmount, message); err != nil {
		return err
	}
	return tx.Commit()
}

// ListStatementFeedback returns an account's responses, newest first.
func (s *Store) ListStatementFeedback(ctx context.Context, neighborID, yearID int64) ([]StatementFeedback, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, status, line_label, line_amount, message, created_at
		  FROM statement_feedback
		 WHERE neighbor_id = $1 AND billing_year_id = $2
		 ORDER BY created_at DESC, id DESC
		 LIMIT 200`, neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatementFeedback{}
	for rows.Next() {
		var f StatementFeedback
		if err := rows.Scan(&f.ID, &f.Status, &f.LineLabel, &f.LineAmount, &f.Message, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
