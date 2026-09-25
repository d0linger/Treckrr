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

// TestLedgerNetIntegration verifies that manual ledger postings net against the
// work bookings in both the per-neighbor sum and the paid/open split.
// Runs only when TEST_DATABASE_URL is set.
func TestLedgerNetIntegration(t *testing.T) {
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

	// See carry_integration_test: static purge first, id-based purge on the way out.
	purgeFixtures(t, ctx, pool, fixtures{Years: []int{2097}, NeighborNames: []string{"Ledger-Nachbar"}})

	baseID, err := st.CreateEmptyBase(ctx, 2097, "Ledger-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2097, baseID, "Ledger-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Ledger-Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	// defer runs before pool.Close(). Children first: since 0039 the year no longer
	// cascades to entries/ledger.
	defer purgeRootsByID(t, ctx, pool, []int64{yearID}, []int64{nid}, []int64{baseID})
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	// Work booking of 100.00.
	cost := decimal.RequireFromString("100")
	e := &models.Entry{NeighborID: nid, BillingYearID: yearID, Date: time.Now(),
		Hours: decimal.RequireFromString("1"), HourlyRate: cost, Cost: cost}
	if _, err := st.CreateEntry(ctx, e, nil); err != nil {
		t.Fatalf("entry: %v", err)
	}
	// Ledger: I owe 30 (credit) and an extra charge of 10 → net ledger -20.
	if _, err := st.AddNeighborLedger(ctx, yearID, nid, decimal.RequireFromString("-30"), "Gegenleistung", time.Now()); err != nil {
		t.Fatalf("ledger credit: %v", err)
	}
	chargeID, err := st.AddNeighborLedger(ctx, yearID, nid, decimal.RequireFromString("10"), "Zuschlag", time.Now())
	if err != nil {
		t.Fatalf("ledger charge: %v", err)
	}

	if sum, err := st.NeighborLedgerSum(ctx, yearID, nid); err != nil || !sum.Equal(decimal.RequireFromString("-20")) {
		t.Fatalf("NeighborLedgerSum = %s, %v; want -20", sum, err)
	}

	// A voided posting must drop out of the sum: void the +10 → net ledger -30.
	if err := st.SetLedgerVoided(ctx, chargeID, true, "Testkorrektur"); err != nil {
		t.Fatalf("void: %v", err)
	}
	if sum, err := st.NeighborLedgerSum(ctx, yearID, nid); err != nil || !sum.Equal(decimal.RequireFromString("-30")) {
		t.Fatalf("after void NeighborLedgerSum = %s, %v; want -30", sum, err)
	}
	// Restore it for the net assertion below.
	if err := st.SetLedgerVoided(ctx, chargeID, false, ""); err != nil {
		t.Fatalf("unvoid: %v", err)
	}

	// Net owed = 100 - 20 = 80. A full payment → paid total 80, open 0.
	if err := st.AddPayment(ctx, yearID, nid, decimal.RequireFromString("80"), time.Now(), "test", ""); err != nil {
		t.Fatalf("add payment: %v", err)
	}
	paid, open, _, err := st.YearPaymentTotals(ctx, yearID)
	if err != nil {
		t.Fatalf("YearPaymentTotals: %v", err)
	}
	if !paid.Equal(decimal.RequireFromString("80")) {
		t.Fatalf("paid = %s, want 80", paid)
	}
	if !open.Equal(decimal.Zero) {
		t.Fatalf("open = %s, want 0", open)
	}

	// NeighborYearHistory (single-query history) must agree: one row for this
	// year with net 80, hours 1, paid, and status carried through.
	history, err := st.NeighborYearHistory(ctx, nid)
	if err != nil {
		t.Fatalf("NeighborYearHistory: %v", err)
	}
	var row *store.NeighborYearHistoryRow
	for i := range history {
		if history[i].YearID == yearID {
			row = &history[i]
		}
	}
	if row == nil {
		t.Fatalf("NeighborYearHistory missing year %d (got %d rows)", yearID, len(history))
	}
	if !row.Net.Equal(decimal.RequireFromString("80")) || !row.Cost.Equal(decimal.RequireFromString("100")) ||
		!row.Ledger.Equal(decimal.RequireFromString("-20")) {
		t.Fatalf("history row cost/ledger/net = %s/%s/%s, want 100/-20/80", row.Cost, row.Ledger, row.Net)
	}
	if !row.Hours.Equal(decimal.RequireFromString("1")) || !row.Paid || row.Year != 2097 {
		t.Fatalf("history row hours/paid/year = %s/%v/%d, want 1/true/2097", row.Hours, row.Paid, row.Year)
	}

	// Orphan guard: postings must block removing the membership row's neighbor.
	if n, err := st.CountLedgerForNeighborYear(ctx, yearID, nid); err != nil || n != 2 {
		t.Fatalf("CountLedgerForNeighborYear = %d, %v; want 2", n, err)
	}
}

// TestLedgerCreditIsNotPaidIntegration pins that a negative rest — the neighbor
// did work for me and "verrechnet" it, no payment yet — is a Guthaben I owe,
// never "Bezahlt". The dashboard and history used Remaining <= 0 as "paid",
// so closing the year showed the unpaid 60 € as settled.
func TestLedgerCreditIsNotPaidIntegration(t *testing.T) {
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

	purgeFixtures(t, ctx, pool, fixtures{Years: []int{2078}, NeighborNames: []string{"Guthaben-Nachbar"}})
	baseID, err := st.CreateEmptyBase(ctx, 2078, "Guthaben-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2078, baseID, "Guthaben-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Guthaben-Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	defer purgeRootsByID(t, ctx, pool, []int64{yearID}, []int64{nid}, []int64{baseID})
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}
	if _, err := st.AddNeighborLedger(ctx, yearID, nid, decimal.RequireFromString("-60"), "Nachbar presst Ballen", time.Now()); err != nil {
		t.Fatalf("ledger credit: %v", err)
	}

	summaries, err := st.YearNeighborSummaries(ctx, yearID)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("YearNeighborSummaries = %d rows, %v; want 1", len(summaries), err)
	}
	if s := summaries[0]; s.Paid || !s.Credit || !s.Remaining.Equal(decimal.RequireFromString("-60")) {
		t.Fatalf("summary paid/credit/remaining = %v/%v/%s, want false/true/-60", s.Paid, s.Credit, s.Remaining)
	}
	history, err := st.NeighborYearHistory(ctx, nid)
	if err != nil || len(history) != 1 {
		t.Fatalf("NeighborYearHistory = %d rows, %v; want 1", len(history), err)
	}
	if h := history[0]; h.Paid || !h.Credit {
		t.Fatalf("history paid/credit = %v/%v, want false/true", h.Paid, h.Credit)
	}

	// Paying the Guthaben out settles the account: Paid, no longer Credit.
	if _, err := st.PayoutCredit(ctx, yearID, nid, time.Now(), "Guthaben ausbezahlt"); err != nil {
		t.Fatalf("payout: %v", err)
	}
	summaries, err = st.YearNeighborSummaries(ctx, yearID)
	if err != nil || len(summaries) != 1 {
		t.Fatalf("after payout YearNeighborSummaries = %d rows, %v", len(summaries), err)
	}
	if s := summaries[0]; !s.Paid || s.Credit {
		t.Fatalf("after payout paid/credit = %v/%v, want true/false", s.Paid, s.Credit)
	}
}
