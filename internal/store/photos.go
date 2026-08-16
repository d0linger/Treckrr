package store

import (
	"context"
	"time"
)

// EntryPhoto is one attached receipt image's metadata (without the bytes).
type EntryPhoto struct {
	ID      int64
	Created time.Time
}

// AddEntryPhoto stores a re-encoded image for a booking and returns its id.
func (s *Store) AddEntryPhoto(ctx context.Context, entryID int64, image []byte, contentType string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		`INSERT INTO entry_photos (entry_id, image, content_type) VALUES ($1,$2,$3) RETURNING id`,
		entryID, image, contentType).Scan(&id)
	return id, err
}

// ListEntryPhotos returns the photo metadata for a booking (newest first).
func (s *Store) ListEntryPhotos(ctx context.Context, entryID int64) ([]EntryPhoto, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, created_at FROM entry_photos WHERE entry_id=$1 ORDER BY created_at DESC, id DESC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntryPhoto
	for rows.Next() {
		var p EntryPhoto
		if err := rows.Scan(&p.ID, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetEntryPhoto returns the image bytes + content type for a photo scoped to its
// booking (so a mismatched entry/photo pair 404s rather than serving cross-links).
func (s *Store) GetEntryPhoto(ctx context.Context, entryID, photoID int64) ([]byte, string, error) {
	var img []byte
	var ct string
	err := s.db.QueryRowContext(ctx,
		`SELECT image, content_type FROM entry_photos WHERE id=$1 AND entry_id=$2`,
		photoID, entryID).Scan(&img, &ct)
	return img, ct, err
}

// DeleteEntryPhoto removes a photo (scoped to its booking).
func (s *Store) DeleteEntryPhoto(ctx context.Context, entryID, photoID int64) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM entry_photos WHERE id=$1 AND entry_id=$2`, photoID, entryID)
	return err
}

// CountEntryPhotos returns how many photos a booking has (for the row badge).
func (s *Store) CountEntryPhotos(ctx context.Context, entryID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM entry_photos WHERE entry_id=$1`, entryID).Scan(&n)
	return n, err
}
