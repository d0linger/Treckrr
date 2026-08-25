package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// AllYearPaymentTotals replaced a YearPaymentTotals-per-year loop on the
// all-years statistics page (an N+1). The batched query must therefore agree with
// the single-year one for EVERY year, including the paid/open/credit split that
// clamps each neighbor's remainder at zero — a naive GROUP BY rewrite would let
// one neighbor's credit cancel another's genuine debt.
// Runs only when TEST_DATABASE_URL is set.
func TestAllYearPaymentTotalsMatchesPerYearIntegration(t *testing.T) {
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

	dec := decimal.RequireFromString

	f := fixtures{
		Years:         []int{2080, 2081, 2082},
		NeighborNames: []string{"Roll-up-Schuldner", "Roll-up-Guthaben"},
	}
	purgeFixtures(t, ctx, pool, f)
	defer purgeFixtures(t, ctx, pool, f)

	baseID, err := st.CreateEmptyBase(ctx, 2080, "Roll-up-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	// Two years, so a per-year bug (rows from one year leaking into another) shows.
	yearA, err := st.CreateBillingYear(ctx, 2080, baseID, "Roll-up-Jahr-A")
	if err != nil {
		t.Fatalf("year A: %v", err)
	}
	yearB, err := st.CreateBillingYear(ctx, 2081, baseID, "Roll-up-Jahr-B")
	if err != nil {
		t.Fatalf("year B: %v", err)
	}
	// A third year with no members at all: it must simply be absent from the map,
	// where the zero value is the right answer.
	yearEmpty, err := st.CreateBillingYear(ctx, 2082, baseID, "Roll-up-Jahr-leer")
	if err != nil {
		t.Fatalf("year empty: %v", err)
	}
	// Two neighbors in year A: one underpaid, one overpaid. Their remainders must
	// NOT net out against each other.
	debtor, err := st.CreateNeighbor(ctx, "Roll-up-Schuldner", "")
	if err != nil {
		t.Fatalf("neighbor debtor: %v", err)
	}
	creditor, err := st.CreateNeighbor(ctx, "Roll-up-Guthaben", "")
	if err != nil {
		t.Fatalf("neighbor creditor: %v", err)
	}
	booking := func(yearID, neighborID int64, amount string) {
		t.Helper()
		c := dec(amount)
		e := &models.Entry{NeighborID: neighborID, BillingYearID: yearID, Date: time.Now(),
			Hours: dec("1"), HourlyRate: c, Cost: c}
		if _, err := st.CreateEntry(ctx, e, nil); err != nil {
			t.Fatalf("entry: %v", err)
		}
	}

	for _, m := range []struct {
		year     int64
		neighbor int64
	}{{yearA, debtor}, {yearA, creditor}, {yearB, debtor}} {
		if err := st.AddNeighborToYear(ctx, m.year, m.neighbor); err != nil {
			t.Fatalf("add neighbor to year: %v", err)
		}
	}

	// Year A: debtor owes 100 and paid 40 (open 60); creditor owes 50 and paid 90
	// (credit 40). A correct roll-up reports paid 130, open 60, credit 40 — not
	// open 20 with the credit silently absorbed.
	booking(yearA, debtor, "100")
	booking(yearA, creditor, "50")
	if err := st.AddPayment(ctx, yearA, debtor, dec("40"), time.Now(), "test"); err != nil {
		t.Fatalf("payment: %v", err)
	}
	if err := st.AddPayment(ctx, yearA, creditor, dec("90"), time.Now(), "test"); err != nil {
		t.Fatalf("payment: %v", err)
	}
	// Year B: a ledger posting so the ledger leg is covered too. 200 - 25 = 175
	// owed, 75 paid, so open 100.
	booking(yearB, debtor, "200")
	if _, err := st.AddNeighborLedger(ctx, yearB, debtor, dec("-25"), "Gegenleistung", time.Now()); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if err := st.AddPayment(ctx, yearB, debtor, dec("75"), time.Now(), "test"); err != nil {
		t.Fatalf("payment: %v", err)
	}

	all, err := st.AllYearPaymentTotals(ctx)
	if err != nil {
		t.Fatalf("AllYearPaymentTotals: %v", err)
	}

	// The absolute values, so the test fails if BOTH implementations drift.
	for _, want := range []struct {
		name               string
		yearID             int64
		paid, open, credit string
	}{
		{"year A", yearA, "130", "60", "40"},
		{"year B", yearB, "75", "100", "0"},
	} {
		got := all[want.yearID]
		if !got.Paid.Equal(dec(want.paid)) || !got.Open.Equal(dec(want.open)) || !got.Credit.Equal(dec(want.credit)) {
			t.Errorf("%s: paid/open/credit = %s/%s/%s, want %s/%s/%s",
				want.name, got.Paid, got.Open, got.Credit, want.paid, want.open, want.credit)
		}
	}

	// And agreement with the per-year query, which is what the refactor replaced.
	for _, yearID := range []int64{yearA, yearB, yearEmpty} {
		paid, open, credit, err := st.YearPaymentTotals(ctx, yearID)
		if err != nil {
			t.Fatalf("YearPaymentTotals(%d): %v", yearID, err)
		}
		got := all[yearID]
		if !got.Paid.Equal(paid) || !got.Open.Equal(open) || !got.Credit.Equal(credit) {
			t.Errorf("year %d: batched %s/%s/%s != per-year %s/%s/%s",
				yearID, got.Paid, got.Open, got.Credit, paid, open, credit)
		}
	}

	if _, present := all[yearEmpty]; present {
		t.Errorf("year with no members should be absent from the map, not a zero row")
	}
}
