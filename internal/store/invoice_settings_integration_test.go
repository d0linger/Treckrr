package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	neturl "net/url"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestInvoiceNumberSettingsAndDate proves the Nummernkreis settings (Nr. 49)
// and the chosen Rechnungsdatum with its § 11 sequence guard (Nr. 48).
//
// It runs against its OWN scratch database: the prefix/start live in the
// shared singleton company row, and setting a prefix there — even briefly —
// would race the exact-number assertions of the snapshot tests running in
// other packages (they'd suddenly issue "IT2091-001" instead of "2091-001").
func TestInvoiceNumberSettingsAndDate(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()

	base, err := neturl.Parse(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	admin := *base
	admin.Path = "/postgres"
	adminPool, err := sql.Open("pgx", admin.String())
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(func() { _ = adminPool.Close() })
	const scratch = "treckrr_settings_test"
	drop := func() {
		_, _ = adminPool.ExecContext(ctx, `DROP DATABASE IF EXISTS `+scratch+` WITH (FORCE)`)
	}
	drop()
	if _, err := adminPool.ExecContext(ctx, `CREATE DATABASE `+scratch); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(drop)
	scratchURL := *base
	scratchURL.Path = "/" + scratch
	pool, err := db.Connect(ctx, scratchURL.String())
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate scratch: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	// Fixtures: one year, three neighbors with § 11-complete data and a booking.
	baseID, err := st.CreateEmptyBase(ctx, 2026, "Settings-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2026, baseID, "Settings-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	if err := st.UpdateCompany(ctx, models.Company{
		Name: "Hof Bergmann", Address: "Feldweg 3", TaxID: "ATU123",
		TaxMode: "pauschal", VATRate: dec("13"),
		InvoicePrefix: "IT", InvoiceStart: 41,
	}); err != nil {
		t.Fatalf("company: %v", err)
	}
	neighbor := func(i int) int64 {
		nid, err := st.CreateNeighbor(ctx, fmt.Sprintf("Settings-Nachbar %d", i), "")
		if err != nil {
			t.Fatalf("neighbor %d: %v", i, err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE neighbors SET address='Dorfstraße 1, 4780' WHERE id=$1`, nid); err != nil {
			t.Fatalf("address: %v", err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
			t.Fatalf("add to year: %v", err)
		}
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(2026, 5, 9), TaskLabel: "Mähen",
			Unit: "h", Hours: dec("2"), HourlyRate: dec("40"), Cost: dec("80.00"),
		}, nil); err != nil {
			t.Fatalf("entry: %v", err)
		}
		return nid
	}
	n1, n2, n3 := neighbor(1), neighbor(2), neighbor(3)

	// Prefix + start + a back-dated first invoice.
	yesterday := time.Now().AddDate(0, 0, -1)
	iv1, err := st.IssueInvoice(ctx, yearID, n1, 2026, yesterday)
	if err != nil {
		t.Fatalf("issue 1: %v", err)
	}
	if iv1.Number != "IT2026-041" {
		t.Errorf("number = %q, want IT2026-041 (prefix + start honored)", iv1.Number)
	}
	if iv1.IssuedOn.Format("2006-01-02") != yesterday.Format("2006-01-02") {
		t.Errorf("issued_on = %s, want the chosen %s", iv1.IssuedOn.Format("2006-01-02"), yesterday.Format("2006-01-02"))
	}

	// Default date (today) continues the sequence.
	iv2, err := st.IssueInvoice(ctx, yearID, n2, 2026, time.Time{})
	if err != nil {
		t.Fatalf("issue 2: %v", err)
	}
	if iv2.Number != "IT2026-042" {
		t.Errorf("number = %q, want IT2026-042", iv2.Number)
	}

	// § 11 sequence guard: a date before the youngest document, or in the
	// future, must refuse.
	if _, err := st.IssueInvoice(ctx, yearID, n3, 2026, yesterday); !errors.Is(err, store.ErrIssueDateInvalid) {
		t.Errorf("back-dating before the youngest document: got %v, want ErrIssueDateInvalid", err)
	}
	if _, err := st.IssueInvoice(ctx, yearID, n3, 2026, time.Now().AddDate(0, 0, 1)); !errors.Is(err, store.ErrIssueDateInvalid) {
		t.Errorf("future date: got %v, want ErrIssueDateInvalid", err)
	}
	if iv3, err := st.IssueInvoice(ctx, yearID, n3, 2026, time.Now()); err != nil || iv3.Number != "IT2026-043" {
		t.Errorf("today must issue as IT2026-043: %v %q", err, iv3.Number)
	}
}
