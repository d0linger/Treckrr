package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// GetCompany returns the single-row company (Absender) settings.
func (s *Store) GetCompany(ctx context.Context) (models.Company, error) {
	var c models.Company
	err := s.db.QueryRowContext(ctx,
		`SELECT name, address, tax_id, tax_note, tax_mode, vat_rate, iban, payment_term_days FROM company WHERE id=1`).
		Scan(&c.Name, &c.Address, &c.TaxID, &c.TaxNote, &c.TaxMode, &c.VATRate, &c.IBAN, &c.PaymentTermDays)
	return c, err
}

// UpdateCompany saves the company (Absender) settings.
func (s *Store) UpdateCompany(ctx context.Context, c models.Company) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE company SET name=$1, address=$2, tax_id=$3, tax_note=$4, tax_mode=$5, vat_rate=$6, iban=$7, payment_term_days=$8 WHERE id=1`,
		c.Name, c.Address, c.TaxID, c.TaxNote, c.TaxMode, c.VATRate, c.IBAN, c.PaymentTermDays)
	return err
}

// invoiceCols is the shared column list for scanning a full invoice (identity +
// frozen Festschreibung snapshot).
const invoiceCols = `id, billing_year_id, neighbor_id, number, issued_on, created_at,
	kind, status, references_invoice_id, payment_reference,
	net, vat_rate, vat_amount, gross, show_vat, tax_mode, tax_note,
	service_from, service_to, issuer, recipient, lines, content_hash`

// scanInvoice reads a full invoice row. The snapshot columns are NULL for legacy
// rows issued before Festschreibung, in which case Content stays nil.
func scanInvoice(sc scanner) (models.Invoice, error) {
	var iv models.Invoice
	var refID sql.NullInt64
	var net, vatRate, vatAmt, gross decimal.NullDecimal
	var showVAT bool
	var taxMode, taxNote, hash string
	var sFrom, sTo sql.NullTime
	var issuer, recipient, lines []byte
	if err := sc.Scan(
		&iv.ID, &iv.BillingYearID, &iv.NeighborID, &iv.Number, &iv.IssuedOn, &iv.Created,
		&iv.Kind, &iv.Status, &refID, &iv.PaymentReference,
		&net, &vatRate, &vatAmt, &gross, &showVAT, &taxMode, &taxNote,
		&sFrom, &sTo, &issuer, &recipient, &lines, &hash,
	); err != nil {
		return iv, err
	}
	if refID.Valid {
		id := refID.Int64
		iv.ReferencesInvoiceID = &id
	}
	if net.Valid { // a Festschreibung snapshot is present
		c := &models.InvoiceContent{
			Net: net.Decimal, VATRate: vatRate.Decimal, VATAmount: vatAmt.Decimal, Gross: gross.Decimal,
			ShowVAT: showVAT, TaxMode: taxMode, TaxNote: taxNote, Hash: hash,
		}
		if sFrom.Valid {
			c.ServiceFrom = sFrom.Time
		}
		if sTo.Valid {
			c.ServiceTo = sTo.Time
		}
		if err := json.Unmarshal(issuer, &c.Issuer); err != nil {
			return iv, fmt.Errorf("scan invoice %d: issuer: %w", iv.ID, err)
		}
		if err := json.Unmarshal(recipient, &c.Recipient); err != nil {
			return iv, fmt.Errorf("scan invoice %d: recipient: %w", iv.ID, err)
		}
		if err := json.Unmarshal(lines, &c.Lines); err != nil {
			return iv, fmt.Errorf("scan invoice %d: lines: %w", iv.ID, err)
		}
		iv.Content = c
	}
	return iv, nil
}

// GetInvoice returns the active issued invoice for a (year, neighbor) — the one
// current, non-canceled Rechnung — or ErrNotFound.
func (s *Store) GetInvoice(ctx context.Context, yearID, neighborID int64) (models.Invoice, error) {
	iv, err := scanInvoice(s.db.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued'`,
		yearID, neighborID))
	if errors.Is(err, sql.ErrNoRows) {
		return iv, ErrNotFound
	}
	return iv, err
}

// BuildInvoiceContent computes the invoice substance from the current live data,
// reproducing exactly what the Beleg shows: USt is charged on the Leistungsentgelt
// (non-voided bookings), shown only for pauschal/regel with a positive rate. This
// is the content IssueInvoice freezes (and the backfill re-freezes).
func (s *Store) BuildInvoiceContent(ctx context.Context, yearID, neighborID int64) (models.InvoiceContent, error) {
	company, err := s.GetCompany(ctx)
	if err != nil {
		return models.InvoiceContent{}, err
	}
	neighbor, err := s.GetNeighbor(ctx, neighborID)
	if err != nil {
		return models.InvoiceContent{}, err
	}
	net, _, err := s.NeighborTotal(ctx, neighborID, yearID)
	if err != nil {
		return models.InvoiceContent{}, err
	}
	entries, err := s.ListEntries(ctx, neighborID, yearID)
	if err != nil {
		return models.InvoiceContent{}, err
	}
	showVAT := (company.TaxMode == "pauschal" || company.TaxMode == "regel") && company.VATRate.IsPositive()
	var vat decimal.Decimal
	if showVAT {
		rate := company.VATRate.Div(decimal.NewFromInt(100))
		vat = net.Mul(rate).Round(2)
	}
	c := models.InvoiceContent{
		Net: net, VATRate: company.VATRate, VATAmount: vat, Gross: net.Add(vat),
		ShowVAT: showVAT, TaxMode: company.TaxMode, TaxNote: company.TaxNote,
		Issuer:    models.InvoiceParty{Name: company.Name, Address: company.Address, TaxID: company.TaxID, IBAN: company.IBAN},
		Recipient: models.InvoiceParty{Name: neighbor.Name, Address: neighbor.Address, TaxID: neighbor.TaxID},
	}
	for _, e := range entries {
		if e.Voided {
			continue
		}
		label := e.TaskLabel
		if label == "" {
			label = e.Note
		}
		if label == "" {
			label = "Sonstige"
		}
		qty, up := e.Hours, e.HourlyRate
		if e.Unit != "" && e.Unit != "h" {
			qty, up = e.Quantity, e.UnitPrice
		}
		c.Lines = append(c.Lines, models.InvoiceLine{
			Date: e.Date, Label: label, Unit: e.Unit, Quantity: qty, UnitPrice: up, Cost: e.Cost,
		})
		if c.ServiceFrom.IsZero() || e.Date.Before(c.ServiceFrom) {
			c.ServiceFrom = e.Date
		}
		if e.Date.After(c.ServiceTo) {
			c.ServiceTo = e.Date
		}
	}
	c.Hash = invoiceContentHash(c)
	return c, nil
}

func invoiceContentHash(c models.InvoiceContent) string {
	b, _ := json.Marshal(c) // Hash field is json:"-", so it is excluded from the digest
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func nullDate(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// InvoicedNeighborIDs returns the set of neighbor IDs that have an active issued
// invoice for the given year. Their bookings are festgeschrieben, so a
// recalculation must skip them (the frozen invoice must never be re-priced).
func (s *Store) InvoicedNeighborIDs(ctx context.Context, yearID int64) (map[int64]bool, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT neighbor_id FROM invoices
		  WHERE billing_year_id=$1 AND kind='invoice' AND status='issued'`, yearID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	set := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		set[id] = true
	}
	return set, rows.Err()
}

// BackfillInvoiceSnapshots freezes a content snapshot for every already-issued
// invoice that predates Festschreibung (net IS NULL). It reproduces each
// invoice's substance from the current live data and writes it into the snapshot
// columns, so the Beleg can render from the frozen record without changing any
// displayed value (the live computation is what the Beleg showed until now).
// Idempotent: rows that already carry a snapshot are skipped. Runs once on boot.
func (s *Store) BackfillInvoiceSnapshots(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, billing_year_id, neighbor_id FROM invoices
		  WHERE net IS NULL AND kind='invoice' ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	type target struct{ id, yearID, neighborID int64 }
	var todo []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.id, &t.yearID, &t.neighborID); err != nil {
			return 0, err
		}
		todo = append(todo, t)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, t := range todo {
		content, err := s.BuildInvoiceContent(ctx, t.yearID, t.neighborID)
		if err != nil {
			return n, fmt.Errorf("backfill invoice %d: %w", t.id, err)
		}
		issuerJSON, _ := json.Marshal(content.Issuer)
		recipientJSON, _ := json.Marshal(content.Recipient)
		linesJSON, _ := json.Marshal(content.Lines)
		if _, err := s.db.ExecContext(ctx, `
			UPDATE invoices SET
			  net=$2, vat_rate=$3, vat_amount=$4, gross=$5, show_vat=$6, tax_mode=$7, tax_note=$8,
			  service_from=$9, service_to=$10, issuer=$11, recipient=$12, lines=$13, content_hash=$14
			WHERE id=$1 AND net IS NULL`,
			t.id,
			content.Net, content.VATRate, content.VATAmount, content.Gross, content.ShowVAT, content.TaxMode, content.TaxNote,
			nullDate(content.ServiceFrom), nullDate(content.ServiceTo), issuerJSON, recipientJSON, linesJSON, content.Hash,
		); err != nil {
			return n, fmt.Errorf("backfill invoice %d: %w", t.id, err)
		}
		n++
	}
	return n, nil
}

// IssueInvoice freezes the current invoice content (Festschreibung) under a
// sequential per-year number and stores it, or returns the existing active
// invoice if one is already issued (idempotent). Content and number are fixed
// once, so the document stays stable regardless of later booking/price changes.
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
	// Serialize number allocation per year (held until commit).
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return models.Invoice{}, err
	}
	// Re-check under the lock: a concurrent request may have just issued it.
	if iv, err := scanInvoice(tx.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued'`,
		yearID, neighborID)); err == nil {
		return iv, tx.Commit()
	} else if !errors.Is(err, sql.ErrNoRows) {
		return models.Invoice{}, err
	}
	// Build the snapshot under the lock and validate the exact content we are about
	// to persist against § 11 — a store-side backstop, so incomplete content can
	// never be frozen even if a caller bypasses the handler's checklist gate.
	content, err := s.BuildInvoiceContent(ctx, yearID, neighborID)
	if err != nil {
		return models.Invoice{}, err
	}
	if len(content.MissingMandatory()) > 0 {
		return models.Invoice{}, ErrInvoiceIncomplete
	}
	// Next sequence = highest existing invoice suffix + 1 (robust to gaps). Only
	// numeric suffixes of kind='invoice' are counted, so storno/gutschrift
	// suffixes (…-S / …-G) can't skew or crash the ::int cast.
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(split_part(number,'-',2)::int), 0)+1
		   FROM invoices
		  WHERE billing_year_id=$1 AND kind='invoice' AND split_part(number,'-',2) ~ '^[0-9]+$'`,
		yearID).Scan(&seq); err != nil {
		return models.Invoice{}, err
	}
	number := fmt.Sprintf("%d-%03d", year, seq)
	iv, err := insertInvoiceDoc(ctx, tx, yearID, neighborID, number, "invoice", nil, content)
	if err != nil {
		return models.Invoice{}, err
	}
	return iv, tx.Commit()
}
