package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestCreateEntryPairIntegration proves the person-with-Gespann booking: one
// call books the machine entry AND its Mannstunden companion in one
// transaction, links the companion to the machine entry, and a full replay of
// the same pair (same idempotency keys) is a no-op on both halves — never a
// third or fourth row.
func TestCreateEntryPairIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	yr := 2102
	personName := fmt.Sprintf("Pair Helfer %d", os.Getpid())
	f := fixtures{Years: []int{yr}, NeighborNames: []string{"Pair Nachbar 2102"}}
	purgePerson := func() { _, _ = pool.ExecContext(ctx, `DELETE FROM persons WHERE name=$1`, personName) }
	purgeFixtures(t, ctx, pool, f)
	purgePerson()
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f); purgePerson() })

	baseID, err := st.CreateEmptyBase(ctx, yr, "Pair-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Pair-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Pair Nachbar 2102", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}
	pid, err := st.CreatePerson(ctx, personName, dec("28.50"), "")
	if err != nil {
		t.Fatalf("person: %v", err)
	}

	mkPair := func() (*models.Entry, *models.Entry) {
		main := &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(yr, 6, 1), TaskLabel: "Mähen",
			Unit: "h", Hours: dec("3"), HourlyRate: dec("40"), Cost: dec("120.00"),
			IdempotencyKey: fmt.Sprintf("pairtest-%d", os.Getpid()),
		}
		comp := &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(yr, 6, 1), TaskLabel: "Mannstunden " + personName,
			Unit: "Mannstunde", Quantity: dec("3"), UnitPrice: dec("28.50"), Cost: dec("85.50"),
			PersonID:       &pid,
			IdempotencyKey: fmt.Sprintf("pairtest-%d-p", os.Getpid()),
		}
		return main, comp
	}

	main, comp := mkPair()
	mainID, compID, err := st.CreateEntryPair(ctx, main, nil, comp)
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	if mainID == 0 || compID == 0 {
		t.Fatalf("pair: expected two fresh ids, got main=%d companion=%d", mainID, compID)
	}
	var linked int64
	if err := pool.QueryRowContext(ctx,
		`SELECT linked_entry_id FROM entries WHERE id=$1`, compID).Scan(&linked); err != nil {
		t.Fatalf("read link: %v", err)
	}
	if linked != mainID {
		t.Fatalf("companion links to %d, want %d", linked, mainID)
	}

	// Replay: both keys exist — no new rows, no error, ids report "already there".
	main2, comp2 := mkPair()
	m2, c2, err := st.CreateEntryPair(ctx, main2, nil, comp2)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if m2 != 0 || c2 != 0 {
		t.Fatalf("replay created rows: main=%d companion=%d", m2, c2)
	}
	var n int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM entries WHERE billing_year_id=$1 AND neighbor_id=$2`, yearID, nid).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("after replay: %d entries, want exactly 2", n)
	}
}

// TestCreditCapAllCreditsIntegration proves Variante A of the credit cap: the
// neighbor+year's issued credit notes — attached AND free together — can never
// exceed the invoice gross, from any direction. Without it, a free Gutschrift
// plus a full attached one drove InvoiceRemaining negative, and the credit
// payout would have paid out cash that never came in.
func TestCreditCapAllCreditsIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")
	lockCompanyRow(t, ctx, pool)

	f := fixtures{Years: []int{2103, 2104}, NeighborNames: []string{"Kappe Nachbar 2103", "Kappe Nachbar 2104"}}
	purgeFixtures(t, ctx, pool, f)
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f) })

	// Shared setup: company + one 218.00 booking → invoice gross 218.00 (no VAT).
	setup := func(t *testing.T, yr int, name string) (yearID, nid int64) {
		t.Helper()
		baseID, err := st.CreateEmptyBase(ctx, yr, "Kappen-Basis")
		if err != nil {
			t.Fatalf("base: %v", err)
		}
		yearID, err = st.CreateBillingYear(ctx, yr, baseID, "Kappen-Jahr")
		if err != nil {
			t.Fatalf("year: %v", err)
		}
		nid, err = st.CreateNeighbor(ctx, name, "")
		if err != nil {
			t.Fatalf("neighbor: %v", err)
		}
		if _, err := pool.ExecContext(ctx, `UPDATE neighbors SET address='Dorfstraße 1, 4780' WHERE id=$1`, nid); err != nil {
			t.Fatalf("neighbor address: %v", err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
			t.Fatalf("add neighbor: %v", err)
		}
		if err := st.UpdateCompany(ctx, models.Company{
			Name: "Hof Kappe", Address: "Feldweg 3", TaxID: "ATU123",
			TaxNote: "Kleinunternehmer", TaxMode: "kleinunternehmer", VATRate: dec("0"),
		}); err != nil {
			t.Fatalf("company: %v", err)
		}
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: day(yr, 5, 9), TaskLabel: "Mähen",
			Unit: "h", Hours: dec("5.45"), HourlyRate: dec("40"), Cost: dec("218.00"),
		}, nil); err != nil {
			t.Fatalf("entry: %v", err)
		}
		return yearID, nid
	}

	t.Run("attached cap counts free credits", func(t *testing.T) {
		yearID, nid := setup(t, 2103, "Kappe Nachbar 2103")
		// Free credit BEFORE the invoice: allowed, there is nothing to cap against.
		if _, err := st.FreeGutschrift(ctx, yearID, nid, 2103, dec("50"), "Vorab"); err != nil {
			t.Fatalf("free gutschrift: %v", err)
		}
		if _, err := st.IssueInvoice(ctx, yearID, nid, 2103, time.Time{}); err != nil {
			t.Fatalf("issue: %v", err)
		}
		// Attached credit over the full gross must now fail: 50 are already credited.
		if _, err := st.GutschriftInvoice(ctx, yearID, nid, dec("218.00"), "voll"); !errors.Is(err, store.ErrGutschriftTooLarge) {
			t.Fatalf("attached over cap: got %v, want ErrGutschriftTooLarge", err)
		}
		// The exact remainder is fine.
		if _, err := st.GutschriftInvoice(ctx, yearID, nid, dec("168.00"), "rest"); err != nil {
			t.Fatalf("attached at cap: %v", err)
		}
		// And nothing more — free path is capped too now that an invoice exists.
		if _, err := st.FreeGutschrift(ctx, yearID, nid, 2103, dec("0.01"), "zuviel"); !errors.Is(err, store.ErrGutschriftTooLarge) {
			t.Fatalf("free over cap: got %v, want ErrGutschriftTooLarge", err)
		}
		// The balance can no longer be negative.
		rest, err := st.InvoiceRemaining(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("remaining: %v", err)
		}
		if rest.IsNegative() {
			t.Fatalf("remaining went negative: %s", rest)
		}
	})

	t.Run("issue refused below already-credited total", func(t *testing.T) {
		yearID, nid := setup(t, 2104, "Kappe Nachbar 2104")
		// Free credit larger than the year's worth of bookings (218.00).
		if _, err := st.FreeGutschrift(ctx, yearID, nid, 2104, dec("500"), "Kulanz"); err != nil {
			t.Fatalf("free gutschrift: %v", err)
		}
		if _, err := st.IssueInvoice(ctx, yearID, nid, 2104, time.Time{}); !errors.Is(err, store.ErrGutschriftTooLarge) {
			t.Fatalf("issue below credits: got %v, want ErrGutschriftTooLarge", err)
		}
	})
}
