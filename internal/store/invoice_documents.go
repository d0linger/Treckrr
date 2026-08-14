package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// insertInvoiceDoc inserts one invoice-family document (invoice / storno /
// gutschrift) with its frozen content and returns it. The document number is used
// as the payment reference. refID links a storno/gutschrift to the invoice it
// corrects (nil for a plain invoice).
func insertInvoiceDoc(ctx context.Context, tx *sql.Tx, yearID, neighborID int64, number, kind string, refID *int64, c models.InvoiceContent) (models.Invoice, error) {
	issuerJSON, _ := json.Marshal(c.Issuer)
	recipientJSON, _ := json.Marshal(c.Recipient)
	linesJSON, _ := json.Marshal(c.Lines)
	return scanInvoice(tx.QueryRowContext(ctx, `
		INSERT INTO invoices
		  (billing_year_id, neighbor_id, number, kind, status, references_invoice_id, payment_reference,
		   net, vat_rate, vat_amount, gross, show_vat, tax_mode, tax_note,
		   service_from, service_to, issuer, recipient, lines, content_hash)
		VALUES ($1,$2,$3,$4,'issued',$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING `+invoiceCols,
		yearID, neighborID, number, kind, refID, number,
		c.Net, c.VATRate, c.VATAmount, c.Gross, c.ShowVAT, c.TaxMode, c.TaxNote,
		nullDate(c.ServiceFrom), nullDate(c.ServiceTo), issuerJSON, recipientJSON, linesJSON, c.Hash))
}

// reverseContent mirrors an invoice's frozen substance into a Storno: the net,
// VAT, gross and each line cost are negated (a full reversal to zero), parties and
// period are kept, and a reference note is prepended. A fresh hash is computed.
func reverseContent(c models.InvoiceContent, origNumber, reason string) models.InvoiceContent {
	rev := c
	rev.Net = c.Net.Neg()
	rev.VATAmount = c.VATAmount.Neg()
	rev.Gross = c.Gross.Neg()
	rev.Lines = make([]models.InvoiceLine, len(c.Lines))
	for i, l := range c.Lines {
		nl := l
		nl.Cost = l.Cost.Neg()
		rev.Lines[i] = nl
	}
	note := "Storno zu Rechnung " + origNumber + "."
	if reason = strings.TrimSpace(reason); reason != "" {
		note += " Grund: " + reason + "."
	}
	rev.TaxNote = strings.TrimSpace(note + " " + c.TaxNote)
	rev.Hash = invoiceContentHash(rev)
	return rev
}

// StornoInvoice cancels the active issued invoice for a neighbor+year by issuing a
// Storno document (kind='storno', number <orig>-S) that fully reverses it, and
// marks the original 'canceled'. This frees the active-invoice slot — unlocking
// the neighbor's basis and returning the Beleg to its live/draft view — so a
// corrected invoice can be re-issued. Returns ErrNotFound if there is no active
// invoice to cancel. The optional reason is recorded on the Storno document.
func (s *Store) StornoInvoice(ctx context.Context, yearID, neighborID int64, reason string) (models.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Invoice{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return models.Invoice{}, err
	}
	orig, err := scanInvoice(tx.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued' FOR UPDATE`,
		yearID, neighborID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.Invoice{}, ErrNotFound
	}
	if err != nil {
		return models.Invoice{}, err
	}
	content, err := s.contentOrBuild(ctx, orig, yearID, neighborID)
	if err != nil {
		return models.Invoice{}, err
	}
	sv, err := insertInvoiceDoc(ctx, tx, yearID, neighborID, orig.Number+"-S", "storno", &orig.ID, reverseContent(content, orig.Number, reason))
	if err != nil {
		return models.Invoice{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invoices SET status='canceled' WHERE id=$1`, orig.ID); err != nil {
		return models.Invoice{}, err
	}
	// A full Storno reverses the whole invoice, so its issued credit notes go with
	// it — never leave active Gutschriften pointing at a canceled original.
	if _, err := tx.ExecContext(ctx,
		`UPDATE invoices SET status='canceled'
		  WHERE references_invoice_id=$1 AND kind='gutschrift' AND status='issued'`,
		orig.ID); err != nil {
		return models.Invoice{}, err
	}
	return sv, tx.Commit()
}

// GutschriftInvoice issues a credit note (kind='gutschrift', number <orig>-G[n])
// that reduces the active invoice by a gross amount (§ 16 UStG Entgeltminderung,
// e.g. a Skonto). The reduction is split into net and VAT at the invoice's own
// rate so the USt correction is booked correctly; for a no-VAT (Kleinunternehmer)
// invoice the whole amount is net. The original invoice stays active — the
// neighbor remains locked and the Gutschrift only lowers what is owed. Amounts are
// stored negative. Returns ErrNotFound if there is no active invoice.
func (s *Store) GutschriftInvoice(ctx context.Context, yearID, neighborID int64, grossReduction decimal.Decimal, note string) (models.Invoice, error) {
	if !grossReduction.IsPositive() {
		return models.Invoice{}, fmt.Errorf("gutschrift: Betrag muss größer 0 sein")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Invoice{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return models.Invoice{}, err
	}
	orig, err := scanInvoice(tx.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued' FOR UPDATE`,
		yearID, neighborID))
	if errors.Is(err, sql.ErrNoRows) {
		return models.Invoice{}, ErrNotFound
	}
	if err != nil {
		return models.Invoice{}, err
	}
	base, err := s.contentOrBuild(ctx, orig, yearID, neighborID)
	if err != nil {
		return models.Invoice{}, err
	}

	// A Gutschrift may not exceed the invoice's remaining (uncredited) gross.
	var creditedGross decimal.Decimal
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(-SUM(gross), 0) FROM invoices
		  WHERE references_invoice_id=$1 AND kind='gutschrift' AND status='issued'`,
		orig.ID).Scan(&creditedGross); err != nil {
		return models.Invoice{}, err
	}
	if grossReduction.GreaterThan(base.Gross.Sub(creditedGross)) {
		return models.Invoice{}, ErrGutschriftTooLarge
	}

	// Split the gross reduction into net + VAT at the invoice's rate, keeping the
	// gross exact (vat = gross − net).
	netRed := grossReduction
	var vatRed decimal.Decimal
	if base.ShowVAT && base.VATRate.IsPositive() {
		factor := decimal.NewFromInt(1).Add(base.VATRate.Div(decimal.NewFromInt(100)))
		netRed = grossReduction.Div(factor).Round(2)
		vatRed = grossReduction.Sub(netRed)
	}
	label := strings.TrimSpace(note)
	if label == "" {
		label = "Entgeltminderung"
	}
	credit := models.InvoiceContent{
		Net: netRed.Neg(), VATRate: base.VATRate, VATAmount: vatRed.Neg(), Gross: grossReduction.Neg(),
		ShowVAT: base.ShowVAT, TaxMode: base.TaxMode,
		TaxNote:     strings.TrimSpace(label + " – § 16 UStG Entgeltminderung zu Rechnung " + orig.Number),
		ServiceFrom: base.ServiceFrom, ServiceTo: base.ServiceTo,
		Issuer: base.Issuer, Recipient: base.Recipient,
		Lines: []models.InvoiceLine{{Date: orig.IssuedOn, Label: label, Cost: netRed.Neg()}},
	}
	credit.Hash = invoiceContentHash(credit)

	// Suffix -G, then -G2, -G3 … for further credit notes on the same invoice.
	var cnt int
	if err := tx.QueryRowContext(ctx,
		`SELECT count(*) FROM invoices WHERE references_invoice_id=$1 AND kind='gutschrift'`,
		orig.ID).Scan(&cnt); err != nil {
		return models.Invoice{}, err
	}
	suffix := "-G"
	if cnt > 0 {
		suffix = fmt.Sprintf("-G%d", cnt+1)
	}
	gv, err := insertInvoiceDoc(ctx, tx, yearID, neighborID, orig.Number+suffix, "gutschrift", &orig.ID, credit)
	if err != nil {
		return models.Invoice{}, err
	}
	return gv, tx.Commit()
}

// contentOrBuild returns the invoice's frozen content, or — for a legacy row whose
// snapshot was never backfilled — reconstructs it from current live data so a
// correction can still be issued. A build failure is propagated rather than
// swallowed, so a Storno/Gutschrift never freezes an empty (zero-amount) document.
func (s *Store) contentOrBuild(ctx context.Context, iv models.Invoice, yearID, neighborID int64) (models.InvoiceContent, error) {
	if iv.Content != nil {
		return *iv.Content, nil
	}
	return s.BuildInvoiceContent(ctx, yearID, neighborID)
}

// ListInvoiceDocuments returns every invoice-family document (invoice, its storno,
// credit notes) for a neighbor+year, oldest first, for the document history and
// settlement. Snapshot-less legacy rows come back with a nil Content.
func (s *Store) ListInvoiceDocuments(ctx context.Context, yearID, neighborID int64) ([]models.Invoice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2
		  ORDER BY id`, yearID, neighborID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.Invoice
	for rows.Next() {
		iv, err := scanInvoice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, iv)
	}
	return out, rows.Err()
}
