package store

import "context"

// SavedView is one named, user-owned allowlisted filter query.
type SavedView struct {
	ID    int64
	Name  string
	Query string
}

// ListSavedViews returns a user's views for one known page scope.
func (s *Store) ListSavedViews(ctx context.Context, userID int64, scope string) ([]SavedView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id,name,query FROM saved_views
		 WHERE user_id=$1 AND scope=$2 ORDER BY name,id`, userID, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]SavedView, 0)
	for rows.Next() {
		var view SavedView
		if err := rows.Scan(&view.ID, &view.Name, &view.Query); err != nil {
			return nil, err
		}
		out = append(out, view)
	}
	return out, rows.Err()
}

// SaveView creates or replaces a same-named view owned by the user.
func (s *Store) SaveView(ctx context.Context, userID int64, scope, name, query string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO saved_views (user_id,scope,name,query) VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id,scope,name) DO UPDATE
		SET query=EXCLUDED.query, updated_at=now()`, userID, scope, name, query)
	return err
}

// DeleteSavedView removes only a view owned by the requesting user.
func (s *Store) DeleteSavedView(ctx context.Context, userID, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM saved_views WHERE id=$1 AND user_id=$2`, id, userID)
	return err
}
