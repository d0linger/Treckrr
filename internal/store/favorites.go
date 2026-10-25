package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"
)

// FavoriteState contains only the authenticated operator's optional preferences.
type FavoriteState struct {
	Enabled bool                `json:"enabled"`
	Items   map[string][]string `json:"items"`
}

// FavoriteChange is an idempotent set/remove operation, not a stale full snapshot.
type FavoriteChange struct {
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Active bool   `json:"active"`
}

// ErrFavoriteInvalid rejects unsupported or oversized preference data.
var ErrFavoriteInvalid = errors.New("invalid favorite")

func validFavorite(c FavoriteChange) bool {
	// The JSON handler already refuses malformed bytes; this keeps the store's
	// own contract for any other caller.
	if !utf8.ValidString(c.Value) || c.Value != strings.TrimSpace(c.Value) || c.Value == "" || utf8.RuneCountInString(c.Value) > 100 {
		return false
	}
	switch c.Kind {
	case "neighbor", "gespann":
		id, err := strconv.ParseInt(c.Value, 10, 64)
		return err == nil && id > 0 && strconv.FormatInt(id, 10) == c.Value
	case "task":
		return !strings.ContainsAny(c.Value, "\r\n\x00")
	default:
		return false
	}
}

// Favorites reads a bounded, user-scoped preference snapshot.
func (s *Store) Favorites(ctx context.Context, userID int64) (FavoriteState, error) {
	state := FavoriteState{Items: map[string][]string{}}
	// Read settings and items from one MVCC snapshot. Separate statements could
	// combine an old disabled flag with a newly committed opt-in/import.
	rows, err := s.db.QueryContext(ctx, `SELECT COALESCE(s.enabled,FALSE),f.kind,f.value
	 FROM (SELECT $1::bigint AS user_id) u
	 LEFT JOIN user_favorite_settings s ON s.user_id=u.user_id
	 LEFT JOIN user_favorites f ON f.user_id=u.user_id
	 ORDER BY f.updated_at DESC,f.value`, userID)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, value sql.NullString
		if err := rows.Scan(&state.Enabled, &kind, &value); err != nil {
			return state, err
		}
		if kind.Valid && value.Valid {
			state.Items[kind.String] = append(state.Items[kind.String], value.String)
		}
	}
	return state, rows.Err()
}

// ChangeFavorites serializes updates per user and merges independent choices.
// Disabled synchronization never accepts background writes; enabling may carry
// the operator's explicit initial import of device-local favorites.
func (s *Store) ChangeFavorites(ctx context.Context, userID int64, enabled *bool, changes []FavoriteChange) error {
	if len(changes) > 24 {
		return ErrFavoriteInvalid
	}
	for _, c := range changes {
		if !validFavorite(c) {
			return ErrFavoriteInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_favorite_settings(user_id) VALUES($1) ON CONFLICT DO NOTHING`, userID); err != nil {
		return err
	}
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT enabled FROM user_favorite_settings WHERE user_id=$1`, userID).Scan(&active); err != nil {
		return err
	}
	if enabled != nil {
		active = *enabled
		if _, err := tx.ExecContext(ctx, `UPDATE user_favorite_settings SET enabled=$2 WHERE user_id=$1`, userID, active); err != nil {
			return err
		}
	}
	if active {
		for _, c := range changes {
			if c.Active {
				_, err = tx.ExecContext(ctx, `INSERT INTO user_favorites(user_id,kind,value) VALUES($1,$2,$3)
				 ON CONFLICT(user_id,kind,value) DO NOTHING`, userID, c.Kind, c.Value)
			} else {
				_, err = tx.ExecContext(ctx, `DELETE FROM user_favorites WHERE user_id=$1 AND kind=$2 AND value=$3`, userID, c.Kind, c.Value)
			}
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM user_favorites WHERE user_id=$1 AND kind=$2 AND value IN
				(SELECT value FROM user_favorites WHERE user_id=$1 AND kind=$2 ORDER BY updated_at DESC,value OFFSET 8)`, userID, c.Kind); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
