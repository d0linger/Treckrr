package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/db"
	"treckrr/internal/models"
	"treckrr/internal/store"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }
func day(y, m, d int) time.Time    { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func hasNeighbor(rows []store.RecalcRow, nid int64) bool {
	for _, r := range rows {
		if r.NeighborID == nid {
			return true
		}
	}
	return false
}

// TestInvoiceSnapshotIntegration proves the Festschreibung: the frozen snapshot
// reproduces the live invoice computation exactly (for all three tax modes), and
// stays immutable when the underlying bookings change afterwards.
func TestInvoiceSnapshotIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	setup := func(t *testing.T, yr int, taxMode string, rate string) (yearID, nid int64, name string) {
		t.Helper()
		baseID, err := st.CreateEmptyBase(ctx, yr, "Rechnungs-Basis")
		if err != nil {
			t.Fatalf("base: %v", err)
		}
		yearID, err = st.CreateBillingYear(ctx, yr, baseID, "Rechnungsjahr")
		if err != nil {
			t.Fatalf("year: %v", err)
		}
		name = fmt.Sprintf("Florian %d", yr) // neighbor names are unique
		nid, err = st.CreateNeighbor(ctx, name, "")
		if err != nil {
			t.Fatalf("neighbor: %v", err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
			t.Fatalf("add neighbor: %v", err)
		}
		if err := st.UpdateCompany(ctx, models.Company{
			Name: "Hof Bergmann", Address: "Feldweg 3", TaxID: "ATU123",
			TaxNote: "§ 22 UStG", TaxMode: taxMode, VATRate: dec(rate),
		}); err != nil {
			t.Fatalf("company: %v", err)
		}
		// Two bookings: an hour line (2.25 h × 40 = 90.00) and a unit line
		// (40 Ballen × 3.20 = 128.00). Net = 218.00.
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(yr, 5, 9), TaskLabel: "Mähen",
			Unit: "h", Hours: dec("2.25"), HourlyRate: dec("40"), Cost: dec("90.00"),
		}, nil); err != nil {
			t.Fatalf("entry1: %v", err)
		}
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(yr, 9, 14), TaskLabel: "Ballenpressen",
			Unit: "Ballen", Quantity: dec("40"), UnitPrice: dec("3.20"), Cost: dec("128.00"),
		}, nil); err != nil {
			t.Fatalf("entry2: %v", err)
		}
		return yearID, nid, name
	}

	t.Run("regel VAT snapshot == live and is immutable", func(t *testing.T) {
		yearID, nid, name := setup(t, 2091, "regel", "13")

		// Live content reproduces the Beleg math: net 218.00, USt 13% = 28.34,
		// brutto 246.34.
		c, err := st.BuildInvoiceContent(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if c.Net.StringFixed(2) != "218.00" || c.VATAmount.StringFixed(2) != "28.34" || c.Gross.StringFixed(2) != "246.34" {
			t.Fatalf("live: net=%s ust=%s brutto=%s", c.Net.StringFixed(2), c.VATAmount.StringFixed(2), c.Gross.StringFixed(2))
		}
		if !c.ShowVAT || len(c.Lines) != 2 || c.Recipient.Name != name || c.Issuer.TaxID != "ATU123" {
			t.Fatalf("content shape wrong: showVAT=%v lines=%d rcpt=%s issuerUID=%s", c.ShowVAT, len(c.Lines), c.Recipient.Name, c.Issuer.TaxID)
		}
		if !c.ServiceFrom.Equal(day(2091, 5, 9)) || !c.ServiceTo.Equal(day(2091, 9, 14)) {
			t.Fatalf("service period: %v..%v", c.ServiceFrom, c.ServiceTo)
		}

		iv, err := st.IssueInvoice(ctx, yearID, nid, 2091)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		if iv.Number != "2091-001" || iv.Kind != "invoice" || iv.Status != "issued" || iv.Content == nil {
			t.Fatalf("issued invoice wrong: %+v", iv)
		}
		if iv.Content.Gross.StringFixed(2) != "246.34" {
			t.Fatalf("snapshot gross = %s", iv.Content.Gross.StringFixed(2))
		}

		// Change the basis AFTER issuing: add a 100.00 booking.
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(2091, 10, 1), TaskLabel: "Nachtrag",
			Unit: "h", Hours: dec("2.5"), HourlyRate: dec("40"), Cost: dec("100.00"),
		}, nil); err != nil {
			t.Fatalf("late entry: %v", err)
		}
		// The live computation now differs …
		c2, err := st.BuildInvoiceContent(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("build after change: %v", err)
		}
		if c2.Net.StringFixed(2) != "318.00" {
			t.Fatalf("live after change should be 318.00, got %s", c2.Net.StringFixed(2))
		}
		// … but the frozen snapshot is unchanged.
		frozen, err := st.GetInvoice(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if frozen.Content == nil {
			t.Fatalf("frozen invoice has no snapshot")
		}
		if frozen.Content.Net.StringFixed(2) != "218.00" || frozen.Content.Gross.StringFixed(2) != "246.34" {
			t.Fatalf("snapshot changed! net=%s gross=%s", frozen.Content.Net.StringFixed(2), frozen.Content.Gross.StringFixed(2))
		}
		// Idempotent re-issue returns the same frozen document.
		again, err := st.IssueInvoice(ctx, yearID, nid, 2091)
		if err != nil {
			t.Fatalf("re-issue: %v", err)
		}
		if again.Content == nil || again.Number != "2091-001" || again.Content.Net.StringFixed(2) != "218.00" {
			t.Fatalf("re-issue changed the document: %s / %+v", again.Number, again.Content)
		}
	})

	t.Run("kleinunternehmer shows no VAT", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2092, "kleinunternehmer", "0")
		c, err := st.BuildInvoiceContent(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if c.ShowVAT || !c.VATAmount.IsZero() || c.Gross.StringFixed(2) != "218.00" {
			t.Fatalf("kleinunternehmer: showVAT=%v ust=%s gross=%s", c.ShowVAT, c.VATAmount, c.Gross.StringFixed(2))
		}
	})

	t.Run("pauschal shows VAT", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2093, "pauschal", "13")
		c, err := st.BuildInvoiceContent(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		if !c.ShowVAT || c.Gross.StringFixed(2) != "246.34" {
			t.Fatalf("pauschal: showVAT=%v gross=%s", c.ShowVAT, c.Gross.StringFixed(2))
		}
	})

	t.Run("backfill re-freezes a legacy invoice at its current live values", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2094, "regel", "13")
		iv, err := st.IssueInvoice(ctx, yearID, nid, 2094)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		want := iv.Content.Gross.StringFixed(2) // 246.34, what the invoice showed

		// Simulate a pre-Festschreibung row: strip the snapshot (net IS NULL is the
		// backfill sentinel). This is exactly the state of invoices issued before
		// migration 0024.
		if _, err := pool.ExecContext(ctx, `UPDATE invoices SET
			net=NULL, vat_rate=NULL, vat_amount=NULL, gross=NULL,
			show_vat=FALSE, tax_mode='', tax_note='',
			service_from=NULL, service_to=NULL, issuer=NULL, recipient=NULL, lines=NULL, content_hash=''
			WHERE id=$1`, iv.ID); err != nil {
			t.Fatalf("strip snapshot: %v", err)
		}
		if legacy, err := st.GetInvoice(ctx, yearID, nid); err != nil {
			t.Fatalf("get legacy: %v", err)
		} else if legacy.Content != nil {
			t.Fatalf("expected nil snapshot after strip, got %+v", legacy.Content)
		}

		n, err := st.BackfillInvoiceSnapshots(ctx)
		if err != nil {
			t.Fatalf("backfill: %v", err)
		}
		if n < 1 {
			t.Fatalf("expected at least one invoice backfilled, got %d", n)
		}

		// The re-frozen snapshot equals what the invoice displayed before → no
		// displayed value changes when the Beleg switches to reading the snapshot.
		filled, err := st.GetInvoice(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("get filled: %v", err)
		}
		if filled.Content == nil || filled.Content.Gross.StringFixed(2) != want {
			t.Fatalf("backfill wrong: want gross %s, got %+v", want, filled.Content)
		}

		// Idempotent: with every invoice now snapshotted, a second pass is a no-op.
		n2, err := st.BackfillInvoiceSnapshots(ctx)
		if err != nil {
			t.Fatalf("backfill2: %v", err)
		}
		if n2 != 0 {
			t.Fatalf("second backfill should be a no-op, got %d", n2)
		}
	})

	t.Run("recalc skips a neighbor with an issued invoice", func(t *testing.T) {
		yearID, aID, _ := setup(t, 2095, "regel", "13")
		// A second neighbor B in the same year, un-invoiced.
		bID, err := st.CreateNeighbor(ctx, "Bernd 2095", "")
		if err != nil {
			t.Fatalf("neighbor B: %v", err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, bID); err != nil {
			t.Fatalf("add B: %v", err)
		}
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: bID, BillingYearID: yearID, Date: day(2095, 6, 1), TaskLabel: "Mähen",
			Unit: "h", Hours: dec("1"), HourlyRate: dec("40"), Cost: dec("40.00"),
		}, nil); err != nil {
			t.Fatalf("entry B: %v", err)
		}

		// Before issuing: nobody is festgeschrieben; the whole-year preview covers both.
		if ids, err := st.InvoicedNeighborIDs(ctx, yearID); err != nil {
			t.Fatalf("invoiced ids: %v", err)
		} else if len(ids) != 0 {
			t.Fatalf("expected no invoiced neighbors, got %v", ids)
		}
		before, err := st.RecalcPreview(ctx, yearID, nil)
		if err != nil {
			t.Fatalf("preview before: %v", err)
		}
		if !hasNeighbor(before, aID) || !hasNeighbor(before, bID) {
			t.Fatalf("preview before should include both A and B")
		}

		// Issue A's invoice → A is festgeschrieben.
		if _, err := st.IssueInvoice(ctx, yearID, aID, 2095); err != nil {
			t.Fatalf("issue A: %v", err)
		}
		if ids, err := st.InvoicedNeighborIDs(ctx, yearID); err != nil {
			t.Fatalf("invoiced ids2: %v", err)
		} else if !ids[aID] || ids[bID] {
			t.Fatalf("expected only A invoiced, got %v", ids)
		}

		// The whole-year recalc now excludes A entirely but still covers B.
		after, err := st.RecalcPreview(ctx, yearID, nil)
		if err != nil {
			t.Fatalf("preview after: %v", err)
		}
		if hasNeighbor(after, aID) {
			t.Fatalf("recalc must skip the invoiced neighbor A")
		}
		if !hasNeighbor(after, bID) {
			t.Fatalf("recalc must still cover the un-invoiced neighbor B")
		}

		// A per-neighbor recalc of the frozen neighbor has nothing to change.
		aRows, err := st.RecalcPreview(ctx, yearID, &aID)
		if err != nil {
			t.Fatalf("preview A: %v", err)
		}
		if len(aRows) != 0 {
			t.Fatalf("recalc of a frozen neighbor should be empty, got %d rows", len(aRows))
		}
	})

	t.Run("storno cancels the invoice, unlocks, and allows re-issue", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2086, "regel", "13")
		iv, err := st.IssueInvoice(ctx, yearID, nid, 2086)
		if err != nil {
			t.Fatalf("issue: %v", err)
		}
		if iv.Number != "2086-001" {
			t.Fatalf("first number = %s", iv.Number)
		}

		sv, err := st.StornoInvoice(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("storno: %v", err)
		}
		if sv.Number != "2086-001-S" || sv.Kind != "storno" || sv.ReferencesInvoiceID == nil || *sv.ReferencesInvoiceID != iv.ID {
			t.Fatalf("storno doc wrong: %+v", sv)
		}
		// The storno fully reverses the original (gross negated to the cent).
		if sv.Content == nil || sv.Content.Gross.StringFixed(2) != "-246.34" {
			t.Fatalf("storno gross = %+v", sv.Content)
		}
		// The active invoice is gone → neighbor unlocked, Beleg back to live.
		if _, err := st.GetInvoice(ctx, yearID, nid); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected no active invoice after storno, got %v", err)
		}
		if ids, err := st.InvoicedNeighborIDs(ctx, yearID); err != nil {
			t.Fatalf("invoiced ids: %v", err)
		} else if ids[nid] {
			t.Fatalf("neighbor should be unlocked after storno")
		}
		// Re-issue picks the next sequence in the year (gapless-ish).
		again, err := st.IssueInvoice(ctx, yearID, nid, 2086)
		if err != nil {
			t.Fatalf("re-issue: %v", err)
		}
		if again.Number != "2086-002" {
			t.Fatalf("re-issued number = %s (want 2086-002)", again.Number)
		}
		// History now holds the original, its storno, and the re-issue.
		docs, err := st.ListInvoiceDocuments(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("docs: %v", err)
		}
		if len(docs) != 3 {
			t.Fatalf("expected 3 documents, got %d", len(docs))
		}
	})

	t.Run("gutschrift splits the VAT and caps at the invoice gross", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2087, "regel", "13")
		if _, err := st.IssueInvoice(ctx, yearID, nid, 2087); err != nil {
			t.Fatalf("issue: %v", err)
		}
		// 24.63 € gross Skonto at 13%: net 21.80, USt 2.83, stored negative.
		g, err := st.GutschriftInvoice(ctx, yearID, nid, dec("24.63"), "3% Skonto")
		if err != nil {
			t.Fatalf("gutschrift: %v", err)
		}
		if g.Number != "2087-001-G" || g.Kind != "gutschrift" {
			t.Fatalf("gutschrift doc wrong: %+v", g)
		}
		if g.Content.Net.StringFixed(2) != "-21.80" || g.Content.VATAmount.StringFixed(2) != "-2.83" || g.Content.Gross.StringFixed(2) != "-24.63" {
			t.Fatalf("gutschrift split wrong: net=%s ust=%s gross=%s", g.Content.Net, g.Content.VATAmount, g.Content.Gross)
		}
		// The original invoice stays active (neighbor stays locked).
		if _, err := st.GetInvoice(ctx, yearID, nid); err != nil {
			t.Fatalf("original should stay active: %v", err)
		}
		// A second credit note gets -G2.
		g2, err := st.GutschriftInvoice(ctx, yearID, nid, dec("10.00"), "")
		if err != nil {
			t.Fatalf("gutschrift2: %v", err)
		}
		if g2.Number != "2087-001-G2" {
			t.Fatalf("second gutschrift number = %s", g2.Number)
		}
		// A credit exceeding the remaining gross is rejected.
		if _, err := st.GutschriftInvoice(ctx, yearID, nid, dec("500.00"), ""); !errors.Is(err, store.ErrGutschriftTooLarge) {
			t.Fatalf("expected ErrGutschriftTooLarge, got %v", err)
		}
	})

	t.Run("gutschrift on a Kleinunternehmer invoice is all net", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2088, "kleinunternehmer", "0")
		if _, err := st.IssueInvoice(ctx, yearID, nid, 2088); err != nil {
			t.Fatalf("issue: %v", err)
		}
		g, err := st.GutschriftInvoice(ctx, yearID, nid, dec("20.00"), "Nachlass")
		if err != nil {
			t.Fatalf("gutschrift: %v", err)
		}
		if g.Content.Net.StringFixed(2) != "-20.00" || !g.Content.VATAmount.IsZero() || g.Content.Gross.StringFixed(2) != "-20.00" {
			t.Fatalf("kleinunternehmer gutschrift: net=%s ust=%s gross=%s", g.Content.Net, g.Content.VATAmount, g.Content.Gross)
		}
	})

	t.Run("storno also cancels the invoice's issued gutschriften", func(t *testing.T) {
		yearID, nid, _ := setup(t, 2085, "regel", "13")
		if _, err := st.IssueInvoice(ctx, yearID, nid, 2085); err != nil {
			t.Fatalf("issue: %v", err)
		}
		g, err := st.GutschriftInvoice(ctx, yearID, nid, dec("24.63"), "Skonto")
		if err != nil {
			t.Fatalf("gutschrift: %v", err)
		}
		if _, err := st.StornoInvoice(ctx, yearID, nid); err != nil {
			t.Fatalf("storno: %v", err)
		}
		// The credit note must not survive as 'issued' against a canceled invoice.
		docs, err := st.ListInvoiceDocuments(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("docs: %v", err)
		}
		for _, d := range docs {
			if d.ID == g.ID && d.Status != "canceled" {
				t.Fatalf("gutschrift %s should be canceled after storno, got %s", d.Number, d.Status)
			}
		}
	})
}
