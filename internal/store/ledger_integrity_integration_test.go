//go:build integration

package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// addYearNeighbor creates another neighbor with an address in the year.
func addYearNeighbor(t *testing.T, st *store.Store, pool *sql.DB, yearID int64, name string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := st.CreateNeighbor(ctx, name, "")
	if err != nil {
		t.Fatalf("neighbor %s: %v", name, err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE neighbors SET address='Review Road 3' WHERE id=$1`, id); err != nil {
		t.Fatalf("neighbor address: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, id); err != nil {
		t.Fatalf("membership %s: %v", name, err)
	}
	return id
}

func flatEntry(yearID, neighborID int64, cost string) *models.Entry {
	return &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: day(2026, 5, 11),
		TaskLabel: "Review flat", Unit: "Pauschale",
		Quantity: decimal.NewFromInt(1), UnitPrice: dec(cost), Cost: dec(cost),
	}
}

// LED-01: one rule blocked by an issued invoice must not starve the others.
func TestRunDueRecurringIsolatesBlockedRuleIntegration(t *testing.T) {
	st, pool, yearID, blockedNeighbor, blockedSource := invoiceFixture(t, false)
	ctx := context.Background()
	freeNeighbor := addYearNeighbor(t, st, pool, yearID, "Recurring free neighbor")
	freeSource, err := st.CreateEntry(ctx, flatEntry(yearID, freeNeighbor, "40"), nil)
	if err != nil {
		t.Fatalf("free source: %v", err)
	}
	tmpl := models.RecurTemplate{
		TaskLabel: "recurring review", Unit: "Pauschale",
		Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(25), Cost: decimal.NewFromInt(25),
	}
	// Both rules are due exactly once today; the blocked rule has the lower id
	// and is processed first (ORDER BY next_run, id).
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	if today.Year() != 2026 {
		t.Skip("fixture billing year is 2026")
	}
	blockedStart, freeStart := today, today
	const ymd = "2006-01-02"
	if err := st.CreateRecurring(ctx, blockedSource, blockedNeighbor, tmpl, "monthly", blockedStart); err != nil {
		t.Fatalf("blocked rule: %v", err)
	}
	if err := st.CreateRecurring(ctx, freeSource, freeNeighbor, tmpl, "monthly", freeStart); err != nil {
		t.Fatalf("free rule: %v", err)
	}
	issueFixtureInvoice(t, st, yearID, blockedNeighbor)

	created, err := st.RunDueRecurring(ctx)
	if err != nil || created != 1 {
		t.Fatalf("tick: created=%d err=%v, want 1/nil", created, err)
	}
	rule := func(neighborID int64) (next time.Time, lastError string) {
		t.Helper()
		if err := pool.QueryRowContext(ctx,
			`SELECT next_run, last_error FROM recurring_entries WHERE neighbor_id=$1`, neighborID).
			Scan(&next, &lastError); err != nil {
			t.Fatalf("rule of %d: %v", neighborID, err)
		}
		return next, lastError
	}
	next, lastError := rule(blockedNeighbor)
	if next.Format(ymd) != today.Format(ymd) || !strings.Contains(lastError, "Rechnung") {
		t.Fatalf("blocked rule: next=%s last_error=%q, want unchanged + invoice reason", next.Format(ymd), lastError)
	}
	next, lastError = rule(freeNeighbor)
	if next.Format(ymd) <= today.Format(ymd) || lastError != "" {
		t.Fatalf("free rule: next=%s last_error=%q, want advanced and clean", next.Format(ymd), lastError)
	}
	rules, err := st.ListRecurring(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rules {
		if r.NeighborID == blockedNeighbor && (r.LastError == "" || r.LastErrorAt == nil) {
			t.Fatalf("ListRecurring hides the waiting state: %+v", r)
		}
	}

	// Storno releases the account: the waiting occurrence books and the
	// reason clears.
	if _, err := st.StornoInvoice(ctx, yearID, blockedNeighbor, "release"); err != nil {
		t.Fatalf("storno: %v", err)
	}
	if created, err := st.RunDueRecurring(ctx); err != nil || created != 1 {
		t.Fatalf("second tick: created=%d err=%v, want 1/nil", created, err)
	}
	if next, lastError := rule(blockedNeighbor); next.Format(ymd) <= today.Format(ymd) || lastError != "" {
		t.Fatalf("released rule: next=%s last_error=%q", next.Format(ymd), lastError)
	}
}

// secondYear adds billing year 2027 with the neighbor as a member.
func secondYear(t *testing.T, st *store.Store, neighborID int64) int64 {
	t.Helper()
	ctx := context.Background()
	baseID, err := st.CreateEmptyBase(ctx, 2027, "Review next")
	if err != nil {
		t.Fatalf("base 2027: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2027, baseID, "Review next")
	if err != nil {
		t.Fatalf("year 2027: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, neighborID); err != nil {
		t.Fatalf("membership 2027: %v", err)
	}
	return yearID
}

// LED-02 and the carry-forward side notes: one transfer side never changes on
// its own, and a completed target year receives nothing.
func TestLedgerTransferSidesAreLockedIntegration(t *testing.T) {
	st, _, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	nextID := secondYear(t, st, neighborID)
	moved, err := st.CarryForwardRemaining(ctx, neighborID, yearID, nextID, time.Now(), "raus", "rein")
	if err != nil || !moved.Equal(dec("100")) {
		t.Fatalf("carry: moved=%s err=%v", moved, err)
	}
	ledger, err := st.ListNeighborLedger(ctx, nextID, neighborID)
	if err != nil || len(ledger) != 1 || ledger[0].TransferID == "" {
		t.Fatalf("target side: %+v (%v)", ledger, err)
	}
	target := ledger[0]
	if err := st.UpdateNeighborLedger(ctx, target.ID, dec("500"), "manipuliert", time.Now()); !errors.Is(err, store.ErrLedgerTransfer) {
		t.Fatalf("update transfer side = %v, want ErrLedgerTransfer", err)
	}
	if err := st.SetLedgerVoided(ctx, target.ID, true, "einseitig"); !errors.Is(err, store.ErrLedgerTransfer) {
		t.Fatalf("void transfer side = %v, want ErrLedgerTransfer", err)
	}
	if err := st.DeleteNeighborLedger(ctx, target.ID); !errors.Is(err, store.ErrLedgerTransfer) {
		t.Fatalf("delete transfer side = %v, want ErrLedgerTransfer", err)
	}
	for _, y := range []int64{yearID, nextID} {
		sum, err := st.NeighborLedgerSum(ctx, y, neighborID)
		if err != nil {
			t.Fatalf("ledger sum: %v", err)
		}
		want := dec("100")
		if y == yearID {
			want = dec("-100")
		}
		if !sum.Equal(want) {
			t.Fatalf("transfer side in year %d changed: %s, want %s", y, sum, want)
		}
	}

	// Carry into a completed year is refused before anything is written.
	if _, err := st.AddNeighborLedger(ctx, yearID, neighborID, dec("50"), "Nachtrag", time.Now()); err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if err := st.SetYearStatus(ctx, nextID, "completed"); err != nil {
		t.Fatalf("complete target: %v", err)
	}
	if _, err := st.CarryForwardRemaining(ctx, neighborID, yearID, nextID, time.Now(), "raus", "rein"); !errors.Is(err, store.ErrCarryTargetCompleted) {
		t.Fatalf("carry into completed year = %v, want ErrCarryTargetCompleted", err)
	}
	if rest, err := st.AccountRemaining(ctx, yearID, neighborID); err != nil || !rest.Equal(dec("50")) {
		t.Fatalf("source after refused carry = %s (%v), want 50", rest, err)
	}
}

// LED-03: before any invoice, free credits are capped at the would-be invoice
// gross less the credits already issued.
func TestFreeGutschriftPreInvoiceCapIntegration(t *testing.T) {
	for _, tc := range []struct {
		name string
		vat  bool
		cap  string // pre-invoice cap for the 100.00 booking: never above its net
	}{{"no vat", false, "100"}, {"vat 20", true, "100"}} {
		t.Run(tc.name, func(t *testing.T) {
			st, _, yearID, neighborID, _ := invoiceFixture(t, tc.vat)
			ctx := context.Background()
			over := dec(tc.cap).Add(dec("0.01"))
			if _, err := st.FreeGutschrift(ctx, yearID, neighborID, 2026, over, "zu viel"); !errors.Is(err, store.ErrGutschriftExceedsBookings) ||
				!errors.Is(err, store.ErrGutschriftTooLarge) {
				t.Fatalf("over cap = %v, want ErrGutschriftExceedsBookings", err)
			}
			if _, err := st.FreeGutschrift(ctx, yearID, neighborID, 2026, dec("60"), "Teil"); err != nil {
				t.Fatalf("first credit: %v", err)
			}
			rest := dec(tc.cap).Sub(dec("60"))
			if _, err := st.FreeGutschrift(ctx, yearID, neighborID, 2026, rest.Add(dec("0.01")), "Rest+"); !errors.Is(err, store.ErrGutschriftExceedsBookings) {
				t.Fatalf("credits summed over cap = %v, want ErrGutschriftExceedsBookings", err)
			}
			if _, err := st.FreeGutschrift(ctx, yearID, neighborID, 2026, rest, "Rest"); err != nil {
				t.Fatalf("credit at cap: %v", err)
			}
			// The balance bills the bookings net until the invoice exists, so a
			// credit up to the cap leaves nothing to pay out, with or without VAT
			// (the gross-only cap left the VAT share as an unbacked Guthaben).
			if payout, err := st.PayoutCredit(ctx, yearID, neighborID, time.Now(), "Guthaben"); err != nil || !payout.IsZero() {
				t.Fatalf("payout of an unbacked credit = %s (%v), want nothing", payout, err)
			}
		})
	}
}

// LED-04: an invoice storno reverses each active attached credit note with its
// own storno document, audited, and leaves the credit's period untouched.
func TestStornoInvoiceReversesAttachedCreditsIntegration(t *testing.T) {
	st, pool, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	iv := issueFixtureInvoice(t, st, yearID, neighborID)
	credit, err := st.GutschriftInvoice(ctx, yearID, neighborID, dec("24"), "Skonto")
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	// Invoice and credit belong to earlier periods than the storno.
	if _, err := pool.ExecContext(ctx, `UPDATE invoices SET issued_on=$1 WHERE id=$2`, day(2026, 1, 15), iv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE invoices SET issued_on=$1 WHERE id=$2`, day(2026, 3, 15), credit.ID); err != nil {
		t.Fatal(err)
	}
	periodSum := func(before time.Time) decimal.Decimal {
		t.Helper()
		journal, err := st.ListInvoiceJournal(ctx, yearID)
		if err != nil {
			t.Fatalf("journal: %v", err)
		}
		sum := decimal.Zero
		for _, row := range journal {
			if row.CountsForRevenue() && row.IssuedOn.Before(before) {
				sum = sum.Add(row.Gross)
			}
		}
		return sum
	}
	cut := day(2026, 4, 1)
	beforeStorno := periodSum(cut)
	if !beforeStorno.Equal(dec("76")) {
		t.Fatalf("pre-storno revenue = %s, want 76", beforeStorno)
	}

	sv, err := st.StornoInvoice(ctx, yearID, neighborID, "Fehler")
	if err != nil {
		t.Fatalf("storno: %v", err)
	}
	if sv.Number != iv.Number+"-S" {
		t.Fatalf("invoice storno number = %s", sv.Number)
	}
	docs, err := st.ListInvoiceDocuments(ctx, yearID, neighborID)
	if err != nil {
		t.Fatalf("documents: %v", err)
	}
	var creditStorno *models.Invoice
	for i, d := range docs {
		switch {
		case d.ID == credit.ID && d.Status != "canceled":
			t.Fatalf("credit %s still %s", d.Number, d.Status)
		case d.Kind == "storno" && d.ReferencesInvoiceID != nil && *d.ReferencesInvoiceID == credit.ID:
			creditStorno = &docs[i]
		}
	}
	if creditStorno == nil || creditStorno.Number != credit.Number+"-S" ||
		creditStorno.Content == nil || !creditStorno.Content.Gross.Equal(dec("24")) {
		t.Fatalf("credit reversal missing or wrong: %+v", creditStorno)
	}
	if creditStorno.ID > sv.ID {
		t.Fatalf("credit reversal %d issued after the invoice storno %d", creditStorno.ID, sv.ID)
	}
	// Old periods keep their revenue; the whole year nets to zero.
	if got := periodSum(cut); !got.Equal(beforeStorno) {
		t.Fatalf("storno changed past periods: %s, want %s", got, beforeStorno)
	}
	if got := periodSum(day(2100, 1, 1)); !got.IsZero() {
		t.Fatalf("year after storno = %s, want 0", got)
	}
	var audits int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE action='document_storno' AND entity='invoice' AND entity_id=$1`,
		itoa(creditStorno.ID)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("credit reversal audit rows = %d (%v), want 1", audits, err)
	}
	if rest, err := st.AccountRemaining(ctx, yearID, neighborID); err != nil || !rest.Equal(dec("100")) {
		t.Fatalf("remaining after storno = %s (%v), want the live 100", rest, err)
	}
}

// LED-05: removal checks every dependent table under the account lock.
func TestRemoveNeighborFromYearGuardIntegration(t *testing.T) {
	st, pool, yearID, _, _ := invoiceFixture(t, false)
	ctx := context.Background()

	withAnzahlung := addYearNeighbor(t, st, pool, yearID, "Remove anzahlung")
	if _, err := st.CreateAnzahlung(ctx, yearID, withAnzahlung, 2026, dec("50"), "1. Abschlag", time.Time{}); err != nil {
		t.Fatalf("anzahlung: %v", err)
	}
	withPlan := addYearNeighbor(t, st, pool, yearID, "Remove plan")
	if _, err := st.AddInstallment(ctx, yearID, withPlan, dec("10"), day(2026, 10, 1), ""); err != nil {
		t.Fatalf("installment: %v", err)
	}
	withDeletedPayment := addYearNeighbor(t, st, pool, yearID, "Remove deleted payment")
	res, err := st.RecordPayment(ctx, store.PaymentInput{YearID: yearID, NeighborID: withDeletedPayment,
		Amount: dec("5"), PaidOn: time.Now()})
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	if ok, err := st.DeletePayment(ctx, res.PaymentID); err != nil || !ok {
		t.Fatalf("delete payment: %v", err)
	}
	for id, kind := range map[int64]string{withAnzahlung: "invoices", withPlan: "payment_plans", withDeletedPayment: "payments"} {
		err := st.RemoveNeighborFromYear(ctx, yearID, id)
		var inUse *store.MembershipInUseError
		if !errors.As(err, &inUse) || inUse.Kind != kind || !errors.Is(err, store.ErrMembershipInUse) {
			t.Fatalf("remove neighbor with %s = %v", kind, err)
		}
		if member, _ := st.NeighborInYear(ctx, yearID, id); !member {
			t.Fatalf("neighbor with %s was removed", kind)
		}
	}

	empty := addYearNeighbor(t, st, pool, yearID, "Remove empty")
	if err := st.RemoveNeighborFromYear(ctx, yearID, empty); err != nil {
		t.Fatalf("remove empty neighbor: %v", err)
	}
	if err := st.RemoveNeighborFromYear(ctx, yearID, empty); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second removal = %v, want ErrNotFound", err)
	}

	// Race: a posting committed while the removal waits for the account lock
	// is seen by the guard instead of being orphaned.
	racer := addYearNeighbor(t, st, pool, yearID, "Remove racer")
	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback() //nolint:errcheck // no-op after Commit
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatal(err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, yearID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- st.RemoveNeighborFromYear(ctx, yearID, racer) }()
	waitForDatabaseBlock(t, ctx, pool, holderPID)
	if _, err := holder.ExecContext(ctx, `
		INSERT INTO neighbor_ledger (billing_year_id, neighbor_id, amount, description, posting_date)
		VALUES ($1,$2,10,'race','2026-05-12')`, yearID, racer); err != nil {
		t.Fatalf("racing posting: %v", err)
	}
	if err := holder.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, store.ErrMembershipInUse) {
		t.Fatalf("removal racing a posting = %v, want ErrMembershipInUse", err)
	}
}

// LED-06: amounts are stored in whole cents; the database refuses sub-cent
// writes but leaves legacy rows operable.
func TestMoneyAmountsAreWholeCentsIntegration(t *testing.T) {
	st, pool, yearID, neighborID, _ := invoiceFixture(t, true)
	ctx := context.Background()
	res, err := st.RecordPayment(ctx, store.PaymentInput{YearID: yearID, NeighborID: neighborID,
		Amount: dec("9.995"), PaidOn: time.Now()})
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	p, err := st.GetPayment(ctx, res.PaymentID)
	if err != nil || !p.Amount.Equal(dec("10")) {
		t.Fatalf("stored payment = %s (%v), want 10.00", p.Amount, err)
	}
	ledgerID, err := st.AddNeighborLedger(ctx, yearID, neighborID, dec("-0.125"), "Rundung", time.Now())
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	_, _, led, err := st.GetLedgerEntry(ctx, ledgerID)
	if err != nil || !led.Amount.Equal(dec("-0.13")) {
		t.Fatalf("stored ledger = %s (%v), want -0.13", led.Amount, err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO payments (billing_year_id, neighbor_id, amount) VALUES ($1,$2,1.005)`,
		yearID, neighborID); err == nil || !strings.Contains(err.Error(), "whole cents") {
		t.Fatalf("raw sub-cent payment insert = %v, want the cents guard", err)
	}
	if _, err := pool.ExecContext(ctx, `UPDATE neighbor_ledger SET amount=1.005 WHERE id=$1`, ledgerID); err == nil {
		t.Fatal("raw sub-cent ledger update was accepted")
	}

	// A legacy sub-cent row (written before the guard) stays operable: voiding,
	// soft-deleting and erasure do not touch its amount.
	for _, q := range []string{
		`ALTER TABLE payments DISABLE TRIGGER payments_amount_cents_ins`,
		`INSERT INTO payments (billing_year_id, neighbor_id, amount) VALUES (` + itoa(yearID) + `,` + itoa(neighborID) + `,0.004)`,
		`ALTER TABLE payments ENABLE TRIGGER payments_amount_cents_ins`,
	} {
		if _, err := pool.ExecContext(ctx, q); err != nil {
			t.Fatalf("legacy fixture %q: %v", q, err)
		}
	}
	var legacyID int64
	if err := pool.QueryRowContext(ctx, `SELECT id FROM payments WHERE amount=0.004`).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.DeletePayment(ctx, legacyID); err != nil || !ok {
		t.Fatalf("soft-delete legacy row: %v", err)
	}

	// Credit notes hash exactly what invoices.gross stores.
	issueFixtureInvoice(t, st, yearID, neighborID)
	gv, err := st.GutschriftInvoice(ctx, yearID, neighborID, dec("10.005"), "halber Cent")
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	var stored decimal.Decimal
	if err := pool.QueryRowContext(ctx, `SELECT gross FROM invoices WHERE id=$1`, gv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !stored.Equal(dec("-10.01")) || !gv.Content.Gross.Equal(stored) {
		t.Fatalf("credit stored %s, content %s, want both -10.01", stored, gv.Content.Gross)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatalf("erasure with a legacy sub-cent row: %v", err)
	}
}

func itoa(v int64) string { return decimal.NewFromInt(v).String() }

// LED-07: payment and Skonto credit are one transaction with pre-validation.
func TestPaymentWithSkontoIsAtomicIntegration(t *testing.T) {
	st, _, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	issueFixtureInvoice(t, st, yearID, neighborID)
	payments := func() int {
		t.Helper()
		list, err := st.ListPayments(ctx, yearID, neighborID)
		if err != nil {
			t.Fatalf("payments: %v", err)
		}
		return len(list)
	}

	// Completed year: the credit note is impossible, so nothing is recorded.
	if err := st.SetYearStatus(ctx, yearID, "completed"); err != nil {
		t.Fatal(err)
	}
	in := store.PaymentInput{YearID: yearID, NeighborID: neighborID, Amount: dec("98"), PaidOn: time.Now(),
		SkontoPct: dec("2")}
	if _, err := st.RecordPayment(ctx, in); !errors.Is(err, store.ErrSkontoYearCompleted) {
		t.Fatalf("skonto in completed year = %v, want ErrSkontoYearCompleted", err)
	}
	if n := payments(); n != 0 {
		t.Fatalf("refused skonto still recorded %d payment(s)", n)
	}
	// Without Skonto the payment side stays open after completion.
	if _, err := st.RecordPayment(ctx, store.PaymentInput{YearID: yearID, NeighborID: neighborID,
		Amount: dec("1"), PaidOn: time.Now()}); err != nil {
		t.Fatalf("plain payment in completed year: %v", err)
	}
	if err := st.SetYearStatus(ctx, yearID, "in_progress"); err != nil {
		t.Fatal(err)
	}

	res, err := st.RecordPayment(ctx, in)
	if err != nil || res.Skonto == nil || !res.Skonto.Content.Gross.Equal(dec("-2")) {
		t.Fatalf("payment with skonto: %+v (%v)", res, err)
	}
	// Over the cap: 98 more credit on a 100 invoice with 2 credited is refused
	// as a whole.
	before := payments()
	if _, err := st.RecordPayment(ctx, store.PaymentInput{YearID: yearID, NeighborID: neighborID,
		Amount: dec("1"), PaidOn: time.Now(), SkontoPct: dec("99")}); !errors.Is(err, store.ErrGutschriftTooLarge) {
		t.Fatalf("skonto over cap = %v, want ErrGutschriftTooLarge", err)
	}
	if payments() != before {
		t.Fatal("refused skonto left a payment behind")
	}
}

// LED-07: the pair hour sync commits with the edit or not at all.
func TestUpdateEntryWithPartnerIsAtomicIntegration(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	pid, err := st.CreatePerson(ctx, "Pair helper", dec("28.50"), "")
	if err != nil {
		t.Fatalf("person: %v", err)
	}
	main := &models.Entry{NeighborID: neighborID, BillingYearID: yearID, Date: day(2026, 6, 1), TaskLabel: "Mähen",
		Unit: "h", Hours: dec("3"), HourlyRate: dec("40"), Cost: dec("120.00")}
	comp := &models.Entry{NeighborID: neighborID, BillingYearID: yearID, Date: day(2026, 6, 1), TaskLabel: "Mannstunden",
		Unit: "Mannstunde", Quantity: dec("3"), UnitPrice: dec("28.50"), Cost: dec("85.50"), PersonID: &pid}
	mainID, compID, err := st.CreateEntryPair(ctx, main, nil, comp)
	if err != nil || mainID == 0 || compID == 0 {
		t.Fatalf("pair: %d/%d %v", mainID, compID, err)
	}
	hoursOf := func(id int64) (hours, cost decimal.Decimal) {
		t.Helper()
		if err := pool.QueryRowContext(ctx, `SELECT hours, cost FROM entries WHERE id=$1`, id).Scan(&hours, &cost); err != nil {
			t.Fatal(err)
		}
		return hours, cost
	}
	loaded, err := st.GetEntry(ctx, compID)
	if err != nil {
		t.Fatal(err)
	}
	// 1.2345 h cannot be mirrored onto the machine (numeric(10,3)): neither
	// half may change.
	edit := *loaded
	edit.Quantity, edit.Cost = dec("1.2345"), dec("35.18")
	if _, err := st.UpdateEntryWithPartner(ctx, &edit, nil, true); !errors.Is(err, store.ErrPairHoursPrecision) {
		t.Fatalf("unrepresentable sync = %v, want ErrPairHoursPrecision", err)
	}
	if q, _ := hoursOf(mainID); !q.Equal(dec("3")) {
		t.Fatalf("machine hours changed to %s", q)
	}
	var compQty decimal.Decimal
	if err := pool.QueryRowContext(ctx, `SELECT quantity FROM entries WHERE id=$1`, compID).Scan(&compQty); err != nil || !compQty.Equal(dec("3")) {
		t.Fatalf("helper edit committed alone: quantity=%s (%v)", compQty, err)
	}

	edit.Quantity, edit.Cost = dec("5"), dec("142.50")
	sync, err := st.UpdateEntryWithPartner(ctx, &edit, nil, true)
	if err != nil || sync.PartnerID != mainID || !sync.Cost.Equal(dec("200")) {
		t.Fatalf("sync: %+v (%v)", sync, err)
	}
	if h, c := hoursOf(mainID); !h.Equal(dec("5")) || !c.Equal(dec("200")) {
		t.Fatalf("machine after sync: %s h / %s", h, c)
	}
}

// WEB-09: a resubmitted payment or installment form books once.
func TestPaymentAndInstallmentIdempotencyIntegration(t *testing.T) {
	st, _, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	paidOn := day(2026, 7, 1)
	in := store.PaymentInput{YearID: yearID, NeighborID: neighborID, Amount: dec("30"), PaidOn: paidOn,
		Method: "bar", IdempotencyKey: "form-key-payment"}
	first, err := st.RecordPayment(ctx, in)
	if err != nil || first.Duplicate {
		t.Fatalf("first: %+v (%v)", first, err)
	}
	again, err := st.RecordPayment(ctx, in)
	if err != nil || !again.Duplicate || again.PaymentID != first.PaymentID {
		t.Fatalf("resubmit: %+v (%v), want the stored payment", again, err)
	}
	changed := in
	changed.Amount = dec("31")
	if _, err := st.RecordPayment(ctx, changed); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("same key, other amount = %v, want ErrIdempotencyConflict", err)
	}
	if list, _ := st.ListPayments(ctx, yearID, neighborID); len(list) != 1 {
		t.Fatalf("payments = %d, want 1", len(list))
	}

	id, dup, err := st.AddInstallmentOnce(ctx, yearID, neighborID, dec("20"), day(2026, 8, 1), "Rate", "form-key-rate")
	if err != nil || dup {
		t.Fatalf("installment: %d %v %v", id, dup, err)
	}
	id2, dup, err := st.AddInstallmentOnce(ctx, yearID, neighborID, dec("20"), day(2026, 8, 1), "Rate", "form-key-rate")
	if err != nil || !dup || id2 != id {
		t.Fatalf("installment resubmit: %d %v %v", id2, dup, err)
	}
	if _, _, err := st.AddInstallmentOnce(ctx, yearID, neighborID, dec("21"), day(2026, 8, 1), "Rate", "form-key-rate"); !errors.Is(err, store.ErrIdempotencyConflict) {
		t.Fatalf("installment conflict = %v", err)
	}
	if plans, _ := st.ListInstallments(ctx, yearID, neighborID); len(plans) != 1 {
		t.Fatalf("installments = %d, want 1", len(plans))
	}
}
