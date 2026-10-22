package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/d0linger/treckrr/internal/models"
)

// ErrSignatureExists reports that the booking is already signed.
var ErrSignatureExists = errors.New("booking already signed")

// insertSignatureTx stores the signature of a booking inserted in the same
// transaction, so a booking and the signature captured with it commit (or
// replay) together.
func insertSignatureTx(ctx context.Context, tx *sql.Tx, entryID int64, e *models.Entry) error {
	sig := e.Signature
	_, err := tx.ExecContext(ctx,
		`INSERT INTO entry_signatures (entry_id, signer_name, signed_at, image, confirmed_text, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		entryID, sig.SignerName, sig.SignedAt, sig.Image, models.SignatureSummary(*e), sig.CreatedBy)
	return err
}

// AddEntrySignature signs an existing booking that is still open for evidence.
func (s *Store) AddEntrySignature(ctx context.Context, e *models.Entry) error {
	yearID, neighborID, err := s.entryAccount(ctx, e.ID)
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
	if err := lockUninvoicedEntry(ctx, tx, e.ID); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM entry_signatures WHERE entry_id=$1)`, e.ID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrSignatureExists
	}
	if err := insertSignatureTx(ctx, tx, e.ID, e); err != nil {
		return err
	}
	return tx.Commit()
}

// GetEntrySignature returns a booking's signature including the image.
func (s *Store) GetEntrySignature(ctx context.Context, entryID int64) (*models.EntrySignature, error) {
	var sig models.EntrySignature
	err := s.db.QueryRowContext(ctx,
		`SELECT id, entry_id, signer_name, signed_at, received_at, image, confirmed_text, created_by
		   FROM entry_signatures WHERE entry_id=$1`, entryID).
		Scan(&sig.ID, &sig.EntryID, &sig.SignerName, &sig.SignedAt, &sig.ReceivedAt, &sig.Image, &sig.ConfirmedText, &sig.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sig, nil
}

// SignaturesByNeighborYear returns the signature metadata (without images) of
// a neighbor's bookings in a year, keyed by entry id.
func (s *Store) SignaturesByNeighborYear(ctx context.Context, neighborID, yearID int64) (map[int64]models.EntrySignature, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.entry_id, s.signer_name, s.signed_at, s.received_at, s.confirmed_text, s.created_by
		   FROM entry_signatures s JOIN entries e ON e.id = s.entry_id
		  WHERE e.neighbor_id=$1 AND e.billing_year_id=$2`, neighborID, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]models.EntrySignature{}
	for rows.Next() {
		var sig models.EntrySignature
		if err := rows.Scan(&sig.ID, &sig.EntryID, &sig.SignerName, &sig.SignedAt, &sig.ReceivedAt, &sig.ConfirmedText, &sig.CreatedBy); err != nil {
			return nil, err
		}
		out[sig.EntryID] = sig
	}
	return out, rows.Err()
}

// DeleteEntrySignature removes the signature of a booking that is not
// festgeschrieben and returns it for the audit trail.
func (s *Store) DeleteEntrySignature(ctx context.Context, entryID int64) (*models.EntrySignature, error) {
	yearID, neighborID, err := s.entryAccount(ctx, entryID)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockMutableBookingAccount(ctx, tx, yearID, neighborID); err != nil {
		return nil, err
	}
	if err := lockUninvoicedEntry(ctx, tx, entryID); err != nil {
		return nil, err
	}
	var sig models.EntrySignature
	err = tx.QueryRowContext(ctx,
		`DELETE FROM entry_signatures WHERE entry_id=$1
		 RETURNING id, entry_id, signer_name, signed_at, received_at, confirmed_text, created_by`, entryID).
		Scan(&sig.ID, &sig.EntryID, &sig.SignerName, &sig.SignedAt, &sig.ReceivedAt, &sig.ConfirmedText, &sig.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &sig, tx.Commit()
}
