package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// AuditActor identifies the authenticated initiator without retaining credentials.
type AuditActor struct {
	UserID       *int64
	Username, IP string
}

type auditActorKey struct{}

// WithAuditActor attaches the actor to mutations that write a transactional audit.
func WithAuditActor(ctx context.Context, actor AuditActor) context.Context {
	if actor.UserID != nil {
		id := *actor.UserID
		actor.UserID = &id
	}
	return context.WithValue(ctx, auditActorKey{}, actor)
}

func addAuditTx(ctx context.Context, tx *sql.Tx, action, entity, entityID, detail string) error {
	actor, _ := ctx.Value(auditActorKey{}).(AuditActor)
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_log (user_id, username, action, entity, entity_id, detail, ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		nullInt(actor.UserID), actor.Username, action, entity, entityID, detail, actor.IP)
	return err
}

// AddAudit records one action in the audit trail.
func (s *Store) AddAudit(ctx context.Context, userID *int64, username, action, entity, entityID, detail, ip string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (user_id, username, action, entity, entity_id, detail, ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		nullInt(userID), username, action, entity, entityID, detail, ip)
	return err
}

// shortLivedAuditActions are pure security/auth/operational events with no tax or
// business-record value — safe to expire on the shorter DSGVO-minimisation window.
// Everything NOT in this set is treated as business/tax-relevant and kept for the
// long window (§ 132 BAO), so a new action defaults to "keep longer" — we never
// silently drop a business event because it wasn't listed.
var shortLivedAuditActions = []string{
	"login", "login_failed", "login_blocked", "login_recovery", "login_2fa_failed",
	"login_passkey", "login_passkey_failed", "logout",
	"rate_limited", "session_revoke", "session_revoke_others",
	"backup_validate", "backup_validate_failed", "backup_download",
	// login_passkey_clone_warning is deliberately NOT here: a possible-clone
	// signal is a security-incident marker that may be needed in an investigation
	// long after routine auth noise is gone, so it keeps the long retention.
}

// PurgeAuditLog applies the staggered retention: short-lived auth/ops/noise events
// older than shortCutoff are deleted; every other (business/tax-relevant) event is
// deleted only past longCutoff. Returns how many rows were removed. Both cutoffs are
// computed by the caller so the policy (windows) lives in one place.
//
// The audit_log table is append-only at the database level (0036: audit_log_guard
// trigger), so the delete is wrapped in a transaction that opts in with
// `SET LOCAL treckrr.allow_audit_prune = 'on'`. SET LOCAL is transaction-scoped and
// reverts on COMMIT/ROLLBACK, so the opt-in can never leak to another query sharing
// the same pooled connection — this deliberate retention job is the only path that
// can delete an audit row.
func (s *Store) PurgeAuditLog(ctx context.Context, shortCutoff, longCutoff time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	// Authorize the delete for THIS transaction only (see the audit_log_guard trigger).
	if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_prune = 'on'`); err != nil {
		return 0, err
	}

	// Build the action set from a comma-separated string that Postgres splits back
	// into rows via string_to_array — no hand-rolled array-literal escaping, so an
	// action containing a brace/quote/backslash can never be mis-parsed. (A comma or
	// NUL in an action name would still split wrong, but audit actions are fixed
	// [a-z_] identifiers; this removes every other escaping footgun.) Passing one
	// text parameter keeps it portable across the database/sql driver without a
	// lib/pq dependency.
	shortCSV := strings.Join(shortLivedAuditActions, ",")
	res, err := tx.ExecContext(ctx, `
		DELETE FROM audit_log
		 WHERE (action = ANY(string_to_array($1, ',')) AND created_at < $2)
		    OR (NOT (action = ANY(string_to_array($1, ','))) AND created_at < $3)`,
		shortCSV, shortCutoff, longCutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// AuditQuery narrows the audit trail. Empty/zero fields mean "no filter", so
// the zero value returns the whole log — the behavior before Nr. 73.
type AuditQuery struct {
	Text     string
	Action   string
	Username string
	From, To time.Time
}

// auditFilter is the shared WHERE clause: action, a case-insensitive substring
// search across the visible columns, the acting user and a date range (Nr. 73 —
// the log grows over years, so "who did what last March" must be answerable).
// $1 = action, $2 = text, $3 = username, $4 = from, $5 = to; every one of them
// disabled when empty/zero. One clause for both the page and the count, so the
// pager can never disagree with the rows.
const auditFilter = `
	WHERE ($1 = '' OR action = $1)
	  AND ($2 = '' OR strpos(
	        lower(concat_ws(' ', username, action, entity, entity_id, detail, ip)),
	        lower($2)) > 0)
	  AND ($3 = '' OR username = $3)
	  AND ($4::timestamptz IS NULL OR created_at >= $4)
	  AND ($5::timestamptz IS NULL OR created_at < $5)`

// auditArgs turns the query into the placeholder values, mapping zero times to
// NULL so the range conditions switch themselves off.
func auditArgs(q AuditQuery) []any {
	var from, to any
	if !q.From.IsZero() {
		from = q.From
	}
	if !q.To.IsZero() {
		// Inclusive day: everything BEFORE the following midnight.
		to = q.To.AddDate(0, 0, 1)
	}
	return []any{q.Action, q.Text, q.Username, from, to}
}

// CountAudit returns the number of audit rows matching the filter.
func (s *Store) CountAudit(ctx context.Context, q AuditQuery) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log`+auditFilter, auditArgs(q)...).Scan(&n)
	return n, err
}

// AuditUsers returns the distinct acting usernames, for the filter dropdown.
func (s *Store) AuditUsers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT username FROM audit_log WHERE username <> '' ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListAuditFiltered returns audit rows matching the filter, newest first. A
// limit <= 0 returns all matching rows (used for CSV export); otherwise the
// page is limit rows starting at offset.
func (s *Store) ListAuditFiltered(ctx context.Context, aq AuditQuery, limit, offset int) ([]models.AuditEntry, error) {
	q := `SELECT id, user_id, username, action, entity, entity_id, detail, ip, created_at
	        FROM audit_log` + auditFilter + ` ORDER BY created_at DESC, id DESC`
	args := auditArgs(aq)
	if limit > 0 {
		q += ` LIMIT $6 OFFSET $7`
		args = append(args, limit, offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAuditRows(rows)
}

// AuditActions returns the distinct action names (for the filter dropdown),
// across the whole audit history.
func (s *Store) AuditActions(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT action FROM audit_log ORDER BY action`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func scanAuditRows(rows *sql.Rows) ([]models.AuditEntry, error) {
	var out []models.AuditEntry
	for rows.Next() {
		var (
			e   models.AuditEntry
			uid sql.NullInt64
		)
		if err := rows.Scan(&e.ID, &uid, &e.Username, &e.Action, &e.Entity,
			&e.EntityID, &e.Detail, &e.IP, &e.Created); err != nil {
			return nil, err
		}
		if uid.Valid {
			id := uid.Int64
			e.UserID = &id
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
