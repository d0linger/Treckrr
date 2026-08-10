package store_test

import (
	"context"
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
		c2, _ := st.BuildInvoiceContent(ctx, yearID, nid)
		if c2.Net.StringFixed(2) != "318.00" {
			t.Fatalf("live after change should be 318.00, got %s", c2.Net.StringFixed(2))
		}
		// … but the frozen snapshot is unchanged.
		frozen, err := st.GetInvoice(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if frozen.Content.Net.StringFixed(2) != "218.00" || frozen.Content.Gross.StringFixed(2) != "246.34" {
			t.Fatalf("snapshot changed! net=%s gross=%s", frozen.Content.Net.StringFixed(2), frozen.Content.Gross.StringFixed(2))
		}
		// Idempotent re-issue returns the same frozen document.
		again, _ := st.IssueInvoice(ctx, yearID, nid, 2091)
		if again.Number != "2091-001" || again.Content.Net.StringFixed(2) != "218.00" {
			t.Fatalf("re-issue changed the document: %s / %s", again.Number, again.Content.Net.StringFixed(2))
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
}
