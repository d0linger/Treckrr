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
	yearID, neighborID, err := s.entryAccount(ctx, entryID)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return 0, err
	}
	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO entry_photos (entry_id, image, content_type) VALUES ($1,$2,$3) RETURNING id`,
		entryID, image, contentType).Scan(&id)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
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
	yearID, neighborID, err := s.entryAccount(ctx, entryID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM entry_photos WHERE id=$1 AND entry_id=$2`, photoID, entryID)
	if err != nil {
		return err
	}
	return tx.Commit()
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
	IsLedger  bool
}

// PhotoCountsForEntries counts receipt photos for exactly the given entries —
// the paged bookings list shows 50 rows and aggregated the WHOLE year before.
func (s *Store) PhotoCountsForEntries(ctx context.Context, ids []int64) (map[int64]int, error) {
	out := map[int64]int{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT entry_id, count(*) FROM entry_photos WHERE entry_id = ANY($1) GROUP BY entry_id`,
		ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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
		`SELECT * FROM (SELECT p.id, e.id AS entry_id, e.entry_date, e.task_label, p.created_at, false AS is_ledger
		   FROM entry_photos p JOIN entries e ON e.id = p.entry_id
		  WHERE e.billing_year_id = $1 AND e.neighbor_id = $2
		 UNION ALL
		 SELECT p.id, l.id, l.posting_date, l.description, p.created_at, true
		   FROM ledger_photos p JOIN neighbor_ledger l ON l.id=p.ledger_id
		  WHERE l.billing_year_id=$1 AND l.neighbor_id=$2) photos
		  ORDER BY entry_date DESC, created_at DESC, id DESC`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PhotoRef
	for rows.Next() {
		var p PhotoRef
		if err := rows.Scan(&p.PhotoID, &p.EntryID, &p.EntryDate, &p.TaskLabel, &p.Created, &p.IsLedger); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
