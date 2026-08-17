package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/d0linger/treckrr/internal/models"
)

// RecordBelegSend appends a send record for a neighbor's Beleg in a year. The
// channel names how it went out ("manuell", "e-mail", …); empty defaults to
// "manuell".
func (s *Store) RecordBelegSend(ctx context.Context, yearID, neighborID int64, channel string) error {
	if channel == "" {
		channel = "manuell"
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO beleg_sends (billing_year_id, neighbor_id, channel) VALUES ($1,$2,$3)`,
		yearID, neighborID, channel)
	return err
}

// DeleteLatestManualBelegSend removes the most recent MANUAL "als versendet"
// mark for a neighbor+year — the Undo of handleBelegMarkSent. It never touches an
// e-mail/mahnung send record (those reflect a real delivery). Returns true when a
// row was deleted, false when there was no manual mark to undo.
func (s *Store) DeleteLatestManualBelegSend(ctx context.Context, yearID, neighborID int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM beleg_sends
		  WHERE id = (SELECT id FROM beleg_sends
		               WHERE billing_year_id=$1 AND neighbor_id=$2 AND channel='manuell'
		               ORDER BY sent_at DESC LIMIT 1)`,
		yearID, neighborID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// LastBelegSend returns the most recent send for a neighbor+year, or (nil, nil)
// if the Beleg was never marked sent.
func (s *Store) LastBelegSend(ctx context.Context, yearID, neighborID int64) (*models.BelegSend, error) {
	var b models.BelegSend
	err := s.db.QueryRowContext(ctx,
		`SELECT id, billing_year_id, neighbor_id, sent_at, channel
		   FROM beleg_sends WHERE billing_year_id=$1 AND neighbor_id=$2
		  ORDER BY sent_at DESC LIMIT 1`, yearID, neighborID).
		Scan(&b.ID, &b.BillingYearID, &b.NeighborID, &b.SentAt, &b.Channel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}
