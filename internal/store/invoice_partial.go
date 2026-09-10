package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// ---- Freie Gutschrift und Anzahlung (Ausbaukarte 53/54) --------------------
//
// Both documents live in the invoices table like every other belegte document,
// but neither occupies the active-invoice slot (the unique index covers
// kind='invoice' only), so bookings stay editable and a Schlussrechnung can
// still be issued afterwards.

// ErrAmountRequired rejects a zero or negative document amount.
var ErrAmountRequired = errors.New("amount must be greater than zero")

// docSeqNumber allocates the next per-year sequence for a document class whose
// number is <prefix><year>-<letter><nnn> (A = Anzahlung, G = freie Gutschrift).
// Must be called inside the transaction that already holds the year's advisory
// lock, so two concurrent requests cannot hand out the same number.
func docSeqNumber(ctx context.Context, tx *sql.Tx, yearID int64, year int, letter string) (string, error) {
	// Only the prefix: invoice_start continues an external INVOICE sequence and
	// has no meaning for the A/G classes, which always start at 1.
	var prefix string
	if err := tx.QueryRowContext(ctx,
		`SELECT invoice_prefix FROM company WHERE id=1`).Scan(&prefix); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	// The sequence is per letter class and independent of the invoice numbers:
	// an Anzahlung is not an invoice, and a gap in one class must not shift the
	// other. substring() pulls the digits after the letter of THIS class only.
	//
	// references_invoice_id IS NULL is what separates a standalone document from
	// an ATTACHED credit note, which GutschriftInvoice numbers <invoice>-G2, -G3 …
	// — those also end in G+digits, so without this filter the first standalone
	// Gutschrift after an attached -G2 would be numbered G003 and skip G001/G002,
	// leaving a permanent gap in a numbered tax sequence.
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(substring(number from $2)::int), 0) + 1
		   FROM invoices
		  WHERE billing_year_id = $1 AND references_invoice_id IS NULL
		    AND number ~ $3`,
		yearID, letter+"([0-9]+)$", letter+"[0-9]+$").Scan(&seq); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%d-%s%03d", prefix, year, letter, seq), nil
}

// creditContent builds the frozen substance of a credit note: the gross
// reduction split into net and VAT at the company's own rate (the whole amount
// is net for a no-VAT setup), stored negative like every other credit note.
func creditContent(company models.Company, neighbor *models.Neighbor, gross decimal.Decimal, label, note string, on time.Time) models.InvoiceContent {
	showVAT := (company.TaxMode == "pauschal" || company.TaxMode == "regel") && company.VATRate.IsPositive()
	net := gross
	var vat decimal.Decimal
	if showVAT {
		factor := decimal.NewFromInt(1).Add(company.VATRate.Div(decimal.NewFromInt(100)))
		net = gross.Div(factor).Round(2)
		vat = gross.Sub(net)
	}
	c := models.InvoiceContent{
		Net: net.Neg(), VATRate: company.VATRate, VATAmount: vat.Neg(), Gross: gross.Neg(),
		ShowVAT: showVAT, TaxMode: company.TaxMode,
		TaxNote:     strings.TrimSpace(note),
		ServiceFrom: on, ServiceTo: on,
		Issuer:    models.InvoiceParty{Name: company.Name, Address: company.Address, TaxID: company.TaxID, IBAN: company.IBAN},
		Recipient: models.InvoiceParty{Name: neighbor.Name, Address: neighbor.Address, TaxID: neighbor.TaxID},
		Lines:     []models.InvoiceLine{{Date: on, Label: label, Cost: net.Neg()}},
	}
	c.Hash = invoiceContentHash(c)
	return c
}

// FreeGutschrift issues a credit note that stands on its own (Ausbaukarte 53):
// no reference invoice required, so a neighbor can be credited after the
// invoice was already stornoed, or for something that never was on one. The
// attached § 16 credit note (GutschriftInvoice) remains the right tool while an
// invoice is active — including for a partial reversal, which IS a Teilstorno
// in substance: an Entgeltminderung on part of the amount.
//
// Like the attached one it lowers InvoiceRemaining (that query sums every
// issued gutschrift of the neighbor+year), and like the attached one it does
// not post to the neighbor ledger — that would double-count the same reduction.
func (s *Store) FreeGutschrift(ctx context.Context, yearID, neighborID int64, year int, gross decimal.Decimal, note string) (models.Invoice, error) {
	if !gross.IsPositive() {
		return models.Invoice{}, ErrAmountRequired
	}
	company, err := s.GetCompany(ctx)
	if err != nil {
		return models.Invoice{}, err
	}
	neighbor, err := s.GetNeighbor(ctx, neighborID)
	if err != nil {
		return models.Invoice{}, err
	}
	label := strings.TrimSpace(note)
	if label == "" {
		label = "Gutschrift"
	}
	now := time.Now()
	content := creditContent(company, neighbor, gross, label,
		strings.TrimSpace(label+" – freie Gutschrift (§ 16 UStG Entgeltminderung)"), now)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Invoice{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return models.Invoice{}, err
	}
	// Same cap as the attached credit note (see ErrGutschriftTooLarge): while an
	// issued invoice exists, the neighbor+year's credits — attached and free
	// together — must not exceed its gross. Without an invoice there is nothing
	// to cap against yet; IssueInvoice then refuses to issue below what was
	// already credited, which closes the loop from the other side.
	var invGross decimal.Decimal
	err = tx.QueryRowContext(ctx,
		`SELECT gross FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='invoice' AND status='issued' FOR UPDATE`,
		yearID, neighborID).Scan(&invGross)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// pre-invoice credit — allowed, bounded at issue time
	case err != nil:
		return models.Invoice{}, err
	default:
		var credited decimal.Decimal
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(-SUM(gross), 0) FROM invoices
			  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='gutschrift' AND status='issued'`,
			yearID, neighborID).Scan(&credited); err != nil {
			return models.Invoice{}, err
		}
		if gross.GreaterThan(invGross.Sub(credited)) {
			return models.Invoice{}, ErrGutschriftTooLarge
		}
	}
	number, err := docSeqNumber(ctx, tx, yearID, year, "G")
	if err != nil {
		return models.Invoice{}, err
	}
	gv, err := insertInvoiceDoc(ctx, tx, yearID, neighborID, number, "gutschrift", nil, now, content)
	if err != nil {
		return models.Invoice{}, err
	}
	return gv, tx.Commit()
}

// CreateAnzahlung issues an Abschlag/Anzahlung (Ausbaukarte 54): a numbered
// request for a partial payment during the season, before the Schlussrechnung
// exists.
//
// DELIBERATELY NOT a USt-Rechnung. A true Anzahlungsrechnung with Steuerausweis
// shifts the USt liability to the receipt of the payment and obliges the
// Endrechnung to deduct the already-taxed Anzahlungen (§ 11 Abs 1 Z 6 UStG).
// Which of the two a farm wants is a tax decision for its operator, not a code
// decision — and the untaxed Abschlag is the variant that leaves every existing
// invariant intact: the Schlussrechnung stays the single tax document at its
// full amount, the journal counts it once, and InvoiceRemaining stays correct
// because the payment against the Abschlag is an ordinary recorded payment.
func (s *Store) CreateAnzahlung(ctx context.Context, yearID, neighborID int64, year int, gross decimal.Decimal, label string, dueOn time.Time) (models.Invoice, error) {
	if !gross.IsPositive() {
		return models.Invoice{}, ErrAmountRequired
	}
	company, err := s.GetCompany(ctx)
	if err != nil {
		return models.Invoice{}, err
	}
	neighbor, err := s.GetNeighbor(ctx, neighborID)
	if err != nil {
		return models.Invoice{}, err
	}
	if label = strings.TrimSpace(label); label == "" {
		label = "Anzahlung"
	}
	now := time.Now()
	if dueOn.IsZero() {
		dueOn = now
	}
	content := models.InvoiceContent{
		Net: gross, VATRate: decimal.Zero, VATAmount: decimal.Zero, Gross: gross,
		ShowVAT: false, TaxMode: company.TaxMode,
		TaxNote: "Abschlag auf die Schlussrechnung. Keine Rechnung im Sinne des § 11 UStG — " +
			"kein Vorsteuerabzug. Die Verrechnung erfolgt mit der Schlussrechnung.",
		ServiceFrom: dueOn, ServiceTo: dueOn,
		Issuer:    models.InvoiceParty{Name: company.Name, Address: company.Address, TaxID: company.TaxID, IBAN: company.IBAN},
		Recipient: models.InvoiceParty{Name: neighbor.Name, Address: neighbor.Address, TaxID: neighbor.TaxID},
		Lines:     []models.InvoiceLine{{Date: dueOn, Label: label, Cost: gross}},
	}
	content.Hash = invoiceContentHash(content)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Invoice{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		return models.Invoice{}, err
	}
	number, err := docSeqNumber(ctx, tx, yearID, year, "A")
	if err != nil {
		return models.Invoice{}, err
	}
	av, err := insertInvoiceDoc(ctx, tx, yearID, neighborID, number, "anzahlung", nil, now, content)
	if err != nil {
		return models.Invoice{}, err
	}
	return av, tx.Commit()
}

// StornoDocument cancels ONE issued non-invoice document (an Anzahlung or a
// free Gutschrift) by id: it issues a reversing document and marks the original
// canceled — never a delete, so the numbered document stays in the journal.
// The active invoice keeps its own path (StornoInvoice), which additionally
// frees the invoice slot and cascades to attached credit notes.
func (s *Store) StornoDocument(ctx context.Context, id int64, reason string) (models.Invoice, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return models.Invoice{}, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	// Advisory lock FIRST, row lock second — the order every sibling document
	// path uses (StornoInvoice, GutschriftInvoice, FreeGutschrift). The old
	// row-then-advisory order deadlocked against StornoInvoice, which holds the
	// advisory lock while canceling attached credit notes by row. The year id
	// is peeked without a lock; the FOR UPDATE re-read below is authoritative
	// and refuses if the document changed in the gap.
	var peekYear int64
	err = tx.QueryRowContext(ctx,
		`SELECT billing_year_id FROM invoices WHERE id=$1`, id).Scan(&peekYear)
	if errors.Is(err, sql.ErrNoRows) {
		return models.Invoice{}, ErrNotFound
	}
	if err != nil {
		return models.Invoice{}, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, peekYear); err != nil {
		return models.Invoice{}, err
	}
	orig, err := scanInvoice(tx.QueryRowContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE id=$1 AND kind IN ('anzahlung','gutschrift') AND status='issued' AND billing_year_id=$2 FOR UPDATE`, id, peekYear))
	if errors.Is(err, sql.ErrNoRows) {
		return models.Invoice{}, ErrNotFound
	}
	if err != nil {
		return models.Invoice{}, err
	}
	// A closed year takes no new documents. The handler cannot pre-check this —
	// it only knows the document id — and every sibling path (StornoInvoice via
	// requireOpenYear, CreateAnzahlung, FreeGutschrift) refuses a completed year,
	// so the guard belongs here, inside the transaction that writes.
	var ystatus string
	if err := tx.QueryRowContext(ctx,
		`SELECT status FROM billing_years WHERE id=$1 FOR UPDATE`, orig.BillingYearID).Scan(&ystatus); err != nil {
		return models.Invoice{}, err
	}
	if ystatus == models.YearCompleted {
		return models.Invoice{}, ErrYearCompleted
	}
	if orig.Content == nil {
		return models.Invoice{}, fmt.Errorf("storno: kein Snapshot vorhanden")
	}
	sv, err := insertInvoiceDoc(ctx, tx, orig.BillingYearID, orig.NeighborID, orig.Number+"-S", "storno",
		&orig.ID, time.Now(), reverseContent(*orig.Content, orig.Number, reason))
	if err != nil {
		return models.Invoice{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE invoices SET status='canceled' WHERE id=$1`, orig.ID); err != nil {
		return models.Invoice{}, err
	}
	return sv, tx.Commit()
}

// ListAnzahlungen returns a neighbor's Abschläge for a year (newest last),
// including canceled ones so the history stays visible.
func (s *Store) ListAnzahlungen(ctx context.Context, yearID, neighborID int64) ([]models.Invoice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+invoiceCols+` FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='anzahlung'
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

// AnzahlungSum is the total still-valid Abschlag amount of a neighbor's year —
// what the Schlussrechnung notes as already requested.
func (s *Store) AnzahlungSum(ctx context.Context, yearID, neighborID int64) (decimal.Decimal, error) {
	var sum decimal.Decimal
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(gross),0) FROM invoices
		  WHERE billing_year_id=$1 AND neighbor_id=$2 AND kind='anzahlung' AND status='issued'`,
		yearID, neighborID).Scan(&sum)
	return sum, err
}
