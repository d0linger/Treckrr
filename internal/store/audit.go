package store

import (
	"context"
	"database/sql"

	"treckrr/internal/models"
)

// AddAudit records one action in the audit trail.
func (s *Store) AddAudit(ctx context.Context, userID *int64, username, action, entity, entityID, detail, ip string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit_log (user_id, username, action, entity, entity_id, detail, ip)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		nullInt(userID), username, action, entity, entityID, detail, ip)
	return err
}

// auditFilter is the shared WHERE clause matching an action filter and a
// case-insensitive substring search across the visible columns. $1 = action
// (empty = all), $2 = query (empty = all). It mirrors the previous in-memory
// filter but runs in SQL so pagination/count cover the whole history.
const auditFilter = `
	WHERE ($1 = '' OR action = $1)
	  AND ($2 = '' OR strpos(
	        lower(concat_ws(' ', username, action, entity, entity_id, detail, ip)),
	        lower($2)) > 0)`

// CountAudit returns the number of audit rows matching the filter.
func (s *Store) CountAudit(ctx context.Context, query, action string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log`+auditFilter, action, query).Scan(&n)
	return n, err
}

// ListAuditFiltered returns audit rows matching the filter, newest first. A
// limit <= 0 returns all matching rows (used for CSV export); otherwise the
// page is limit rows starting at offset.
func (s *Store) ListAuditFiltered(ctx context.Context, query, action string, limit, offset int) ([]models.AuditEntry, error) {
	q := `SELECT id, user_id, username, action, entity, entity_id, detail, ip, created_at
	        FROM audit_log` + auditFilter + ` ORDER BY created_at DESC, id DESC`
	args := []any{action, query}
	if limit > 0 {
		q += ` LIMIT $3 OFFSET $4`
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
