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
