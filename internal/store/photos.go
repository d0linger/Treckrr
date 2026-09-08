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

// ---- Fotos sichtbar machen (Ausbaukarte 74) --------------------------------

// PhotoRef is one receipt photo with the booking it belongs to, for the
// per-neighbor gallery — the images themselves were only reachable from a
// booking's edit page before.
type PhotoRef struct {
	PhotoID   int64
	EntryID   int64
	EntryDate time.Time
	TaskLabel string
	Created   time.Time
}

// PhotoCounts returns how many photos each booking of a year has, keyed by
// entry id. neighborID 0 covers the whole year. One query instead of one per
// row: the list renders 50 bookings at a time.
func (s *Store) PhotoCounts(ctx context.Context, yearID, neighborID int64) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.entry_id, count(*)
		   FROM entry_photos p JOIN entries e ON e.id = p.entry_id
		  WHERE e.billing_year_id = $1 AND ($2 = 0 OR e.neighbor_id = $2)
		  GROUP BY p.entry_id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ListNeighborPhotos returns every receipt photo of a neighbor's year, newest
// booking first, for the gallery.
func (s *Store) ListNeighborPhotos(ctx context.Context, yearID, neighborID int64) ([]PhotoRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT p.id, e.id, e.entry_date, e.task_label, p.created_at
		   FROM entry_photos p JOIN entries e ON e.id = p.entry_id
		  WHERE e.billing_year_id = $1 AND e.neighbor_id = $2
		  ORDER BY e.entry_date DESC, p.id DESC`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PhotoRef
	for rows.Next() {
		var p PhotoRef
		if err := rows.Scan(&p.PhotoID, &p.EntryID, &p.EntryDate, &p.TaskLabel, &p.Created); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
