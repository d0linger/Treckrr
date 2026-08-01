package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"treckrr/internal/models"
)

// GetCompany returns the single-row company (Absender) settings.
func (s *Store) GetCompany(ctx context.Context) (models.Company, error) {
	var c models.Company
	err := s.db.QueryRowContext(ctx,
		`SELECT name, address, tax_id, tax_note, tax_mode, vat_rate FROM company WHERE id=1`).
		Scan(&c.Name, &c.Address, &c.TaxID, &c.TaxNote, &c.TaxMode, &c.VATRate)
	return c, err
}

// UpdateCompany saves the company (Absender) settings.
func (s *Store) UpdateCompany(ctx context.Context, c models.Company) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE company SET name=$1, address=$2, tax_id=$3, tax_note=$4, tax_mode=$5, vat_rate=$6 WHERE id=1`,
		c.Name, c.Address, c.TaxID, c.TaxNote, c.TaxMode, c.VATRate)
	return err
}

// GetInvoice returns the issued invoice for a (year, neighbor), or ErrNotFound.
func (s *Store) GetInvoice(ctx context.Context, yearID, neighborID int64) (models.Invoice, error) {
	var iv models.Invoice
	err := s.db.QueryRowContext(ctx,
		`SELECT id, billing_year_id, neighbor_id, number, issued_on, created_at
		   FROM invoices WHERE billing_year_id=$1 AND neighbor_id=$2`, yearID, neighborID).
		Scan(&iv.ID, &iv.BillingYearID, &iv.NeighborID, &iv.Number, &iv.IssuedOn, &iv.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return iv, ErrNotFound
	}
	return iv, err
}

// IssueInvoice assigns the next sequential number within the year (e.g.
// "2026-001") and stores it, or returns the existing invoice if already issued.
// The number is fixed once, so the document stays stable.
func (s *Store) IssueInvoice(ctx context.Context, yearID, neighborID int64, year int) (models.Invoice, error) {
	if iv, err := s.GetInvoice(ctx, yearID, neighborID); err == nil {
		return iv, nil
	} else if !errors.Is(err, ErrNotFound) {
		return models.Invoice{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Invoice{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Serialize number allocation per year so two concurrent issues (even for
	// different neighbors) can't be handed the same sequence. The lock is held
	// until Commit/Rollback.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return models.Invoice{}, err
	}
	// Re-check under the lock: a concurrent request may have just issued it.
	var existing models.Invoice
	err = tx.QueryRowContext(ctx,
		`SELECT id, billing_year_id, neighbor_id, number, issued_on, created_at
		   FROM invoices WHERE billing_year_id=$1 AND neighbor_id=$2`, yearID, neighborID).
		Scan(&existing.ID, &existing.BillingYearID, &existing.NeighborID, &existing.Number, &existing.IssuedOn, &existing.Created)
	if err == nil {
		return existing, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return models.Invoice{}, err
	}
	// Next sequence = highest existing suffix + 1 (robust to gaps, unlike count).
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(NULLIF(split_part(number,'-',2),'')::int), 0)+1
		   FROM invoices WHERE billing_year_id=$1`, yearID).Scan(&seq); err != nil {
		return models.Invoice{}, err
	}
	number := fmt.Sprintf("%d-%03d", year, seq)
	var iv models.Invoice
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO invoices (billing_year_id, neighbor_id, number) VALUES ($1,$2,$3)
		 RETURNING id, billing_year_id, neighbor_id, number, issued_on, created_at`,
		yearID, neighborID, number).
		Scan(&iv.ID, &iv.BillingYearID, &iv.NeighborID, &iv.Number, &iv.IssuedOn, &iv.Created); err != nil {
		return models.Invoice{}, err
	}
	return iv, tx.Commit()
}
