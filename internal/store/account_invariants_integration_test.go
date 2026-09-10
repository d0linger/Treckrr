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

func invoiceFixture(t *testing.T, vat bool) (*store.Store, *sql.DB, int64, int64, int64) {
	t.Helper()
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	lockCompanyRow(t, ctx, pool)
	company := models.Company{
		Name:         "Review Farm",
		Address:      "Review Street 1",
		TaxMode:      "kleinunternehmer",
		InvoiceStart: 1,
	}
	if vat {
		company.TaxMode = "regel"
		company.VATRate = decimal.NewFromInt(20)
		company.TaxID = "ATU12345678"
	}
	if err := st.UpdateCompany(ctx, company); err != nil {
		t.Fatalf("company: %v", err)
	}
	if _, err := pool.ExecContext(ctx,
		`UPDATE neighbors SET address='Review Road 2' WHERE id=$1`, neighborID); err != nil {
		t.Fatalf("neighbor address: %v", err)
	}
	entryID, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID,
		Date:      time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		TaskLabel: "Review work", Unit: "Pauschale",
		Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(100), Cost: decimal.NewFromInt(100),
	}, nil)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	return st, pool, yearID, neighborID, entryID
}

func issueFixtureInvoice(t *testing.T, st *store.Store, yearID, neighborID int64) models.Invoice {
	t.Helper()
	iv, err := st.IssueInvoice(context.Background(), yearID, neighborID, 2026,
		time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("issue invoice: %v", err)
	}
	if iv.Content == nil {
		t.Fatal("issued invoice has no frozen content")
	}
	return iv
}

func TestInvoiceGrossIsAuthoritativeForSettlement(t *testing.T) {
	t.Run("exact gross payment cannot create a phantom payout", func(t *testing.T) {
		st, _, yearID, neighborID, _ := invoiceFixture(t, true)
		iv := issueFixtureInvoice(t, st, yearID, neighborID)
		if err := st.AddPayment(context.Background(), yearID, neighborID, iv.Content.Gross,
			time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), "", "bank"); err != nil {
			t.Fatalf("payment: %v", err)
		}
		remaining, err := st.AccountRemaining(context.Background(), yearID, neighborID)
		if err != nil {
			t.Fatalf("remaining: %v", err)
		}
		if !remaining.IsZero() {
			t.Fatalf("remaining = %s, want zero after paying frozen gross", remaining)
		}
		payout, err := st.PayoutCredit(context.Background(), yearID, neighborID,
			time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), "review payout")
		if err != nil {
			t.Fatalf("payout: %v", err)
		}
		if !payout.IsZero() {
			t.Fatalf("phantom payout = %s, want zero", payout)
		}
	})

	t.Run("settle books frozen gross including VAT", func(t *testing.T) {
		st, _, yearID, neighborID, _ := invoiceFixture(t, true)
		iv := issueFixtureInvoice(t, st, yearID, neighborID)
		settled, err := st.SettleRemaining(context.Background(), yearID, neighborID,
			time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), "review settlement")
		if err != nil {
			t.Fatalf("settle: %v", err)
		}
		if !settled.Equal(iv.Content.Gross) {
			t.Fatalf("settled = %s, want frozen gross %s", settled, iv.Content.Gross)
		}
		remaining, err := st.InvoiceRemaining(context.Background(), yearID, neighborID)
		if err != nil {
			t.Fatalf("invoice remaining: %v", err)
		}
		if !remaining.IsZero() {
			t.Fatalf("invoice remaining = %s, want zero", remaining)
		}
	})

	t.Run("credit note reduces year history payable", func(t *testing.T) {
		st, _, yearID, neighborID, _ := invoiceFixture(t, false)
		iv := issueFixtureInvoice(t, st, yearID, neighborID)
		credit, err := st.GutschriftInvoice(context.Background(), yearID, neighborID, decimal.NewFromInt(25), "review credit")
		if err != nil {
			t.Fatalf("credit note: %v", err)
		}
		wantPayable := iv.Content.Gross.Add(credit.Content.Gross)
		if err := st.AddPayment(context.Background(), yearID, neighborID, wantPayable,
			time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), "", "bank"); err != nil {
			t.Fatalf("payment: %v", err)
		}
		history, err := st.NeighborYearHistory(context.Background(), neighborID)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		for _, row := range history {
			if row.YearID != yearID {
				continue
			}
			if !row.Payable.Equal(wantPayable) || !row.Remaining.IsZero() || !row.Paid {
				t.Fatalf("history payable/remaining/paid = %s/%s/%v, want %s/0/true",
					row.Payable, row.Remaining, row.Paid, wantPayable)
			}
			return
		}
		t.Fatalf("history missing billing year %d", yearID)
	})
}

func waitForLockWaiters(t *testing.T, ctx context.Context, pool *sql.DB, holderPID, minimum int) {
	t.Helper()
	for {
		var count int
		if err := pool.QueryRowContext(ctx, `WITH RECURSIVE waiters(pid) AS (
			SELECT pid FROM pg_stat_activity
			 WHERE datname=current_database() AND wait_event_type='Lock'
			   AND $1 = ANY(pg_blocking_pids(pid))
			UNION
			SELECT a.pid FROM pg_stat_activity a JOIN waiters w
			  ON w.pid = ANY(pg_blocking_pids(a.pid))
			 WHERE a.datname=current_database() AND a.wait_event_type='Lock'
		)
		SELECT count(*) FROM waiters`, holderPID).Scan(&count); err != nil {
			t.Fatalf("count lock waiters: %v", err)
		}
		if count >= minimum {
			return
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("wanted at least %d lock waiters: %v", minimum, ctx.Err())
		}
	}
}

func TestInvoiceFreezeSerializesBookingWriter(t *testing.T) {
	st, pool, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer func() { _ = holder.Rollback() }()
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("holder pid: %v", err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT 1 FROM billing_year_neighbors
		WHERE billing_year_id=$1 AND neighbor_id=$2 FOR UPDATE`, yearID, neighborID); err != nil {
		t.Fatalf("hold account: %v", err)
	}

	type invoiceResult struct {
		invoice models.Invoice
		err     error
	}
	invoiceDone := make(chan invoiceResult, 1)
	go func() {
		iv, err := st.IssueInvoice(ctx, yearID, neighborID, 2026,
			time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))
		invoiceDone <- invoiceResult{invoice: iv, err: err}
	}()
	waitForDatabaseBlock(t, ctx, pool, holderPID)

	bookingDone := make(chan error, 1)
	go func() {
		_, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: neighborID, BillingYearID: yearID,
			Date:      time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
			TaskLabel: "racing work", Unit: "Pauschale",
			Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(50), Cost: decimal.NewFromInt(50),
		}, nil)
		bookingDone <- err
	}()
	waitForLockWaiters(t, ctx, pool, holderPID, 2)
	if err := holder.Commit(); err != nil {
		t.Fatalf("release holder: %v", err)
	}

	issued := <-invoiceDone
	if issued.err != nil {
		t.Fatalf("issue: %v", issued.err)
	}
	if err := <-bookingDone; !errors.Is(err, store.ErrInvoiceLocked) {
		t.Fatalf("racing booking error = %v, want ErrInvoiceLocked", err)
	}
	live, _, err := st.NeighborTotal(context.Background(), neighborID, yearID)
	if err != nil {
		t.Fatalf("live total: %v", err)
	}
	if !issued.invoice.Content.Net.Equal(live) || !live.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("frozen/live net = %s/%s, want 100/100", issued.invoice.Content.Net, live)
	}
}

func TestRecurringAndReplayRespectInvoiceFreeze(t *testing.T) {
	t.Run("blocked recurrence stays pending", func(t *testing.T) {
		st, pool, yearID, neighborID, entryID := invoiceFixture(t, false)
		ctx := context.Background()
		today := time.Date(2026, 9, 10, 0, 0, 0, 0, time.Local)
		tmpl := models.RecurTemplate{
			TaskLabel: "recurring review", Unit: "Pauschale",
			Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(25), Cost: decimal.NewFromInt(25),
		}
		if err := st.CreateRecurring(ctx, entryID, neighborID, tmpl, "monthly", today); err != nil {
			t.Fatalf("create recurring: %v", err)
		}
		var ruleID int64
		var before time.Time
		if err := pool.QueryRowContext(ctx,
			`SELECT id, next_run FROM recurring_entries WHERE neighbor_id=$1`, neighborID).Scan(&ruleID, &before); err != nil {
			t.Fatalf("load recurring: %v", err)
		}
		issueFixtureInvoice(t, st, yearID, neighborID)
		created, err := st.RunDueRecurring(ctx)
		if !errors.Is(err, store.ErrInvoiceLocked) || created != 0 {
			t.Fatalf("due recurrence: created=%d err=%v, want 0/ErrInvoiceLocked", created, err)
		}
		var after time.Time
		if err := pool.QueryRowContext(ctx,
			`SELECT next_run FROM recurring_entries WHERE id=$1`, ruleID).Scan(&after); err != nil {
			t.Fatalf("reload recurring: %v", err)
		}
		if !after.Equal(before) {
			t.Fatalf("blocked occurrence advanced from %v to %v", before, after)
		}
	})

	t.Run("acknowledged replay is a scoped no-op", func(t *testing.T) {
		st, _, yearID, neighborID, _ := invoiceFixture(t, false)
		ctx := context.Background()
		entry := func(neighbor int64) *models.Entry {
			return &models.Entry{
				NeighborID: neighbor, BillingYearID: yearID,
				Date:      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
				TaskLabel: "offline review", Unit: "Pauschale",
				Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10), Cost: decimal.NewFromInt(10),
				IdempotencyKey: "review-offline-key",
			}
		}
		if id, err := st.CreateEntry(ctx, entry(neighborID), nil); err != nil || id == 0 {
			t.Fatalf("initial offline entry: id=%d err=%v", id, err)
		}
		issueFixtureInvoice(t, st, yearID, neighborID)
		if id, err := st.CreateEntry(ctx, entry(neighborID), nil); err != nil || id != 0 {
			t.Fatalf("acknowledged replay: id=%d err=%v, want no-op", id, err)
		}
		otherID, err := st.CreateNeighbor(ctx, "Other review neighbor", "")
		if err != nil {
			t.Fatalf("other neighbor: %v", err)
		}
		if err := st.AddNeighborToYear(ctx, yearID, otherID); err != nil {
			t.Fatalf("other membership: %v", err)
		}
		if _, err := st.CreateEntry(ctx, entry(otherID), nil); !errors.Is(err, store.ErrIdempotencyConflict) {
			t.Fatalf("cross-account replay error = %v, want ErrIdempotencyConflict", err)
		}
	})
}

func TestRecalcCannotCommitAfterInvoiceFreeze(t *testing.T) {
	st, pool, yearID, neighborID, entryID := invoiceFixture(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	year, err := st.GetBillingYear(ctx, yearID)
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	machineID, err := st.CreateMachine(ctx, year.BaseID, "review machine",
		decimal.NewFromInt(1), decimal.NewFromInt(100), "", 0, decimal.Zero)
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	entry, err := st.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	entry.Unit = "h"
	entry.Hours = decimal.NewFromInt(1)
	entry.Quantity = decimal.NewFromInt(1)
	entry.HourlyRate = decimal.NewFromInt(100)
	entry.UnitPrice = decimal.NewFromInt(100)
	entry.Cost = decimal.NewFromInt(100)
	if err := st.UpdateEntry(ctx, entry, []int64{machineID}); err != nil {
		t.Fatalf("prepare entry: %v", err)
	}
	if err := st.UpdateMachine(ctx, machineID, "review machine",
		decimal.NewFromInt(1), decimal.NewFromInt(200), "", 0, decimal.Zero); err != nil {
		t.Fatalf("reprice machine: %v", err)
	}

	holder, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("holder: %v", err)
	}
	defer func() { _ = holder.Rollback() }()
	var holderPID int
	if err := holder.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID); err != nil {
		t.Fatalf("holder pid: %v", err)
	}
	if _, err := holder.ExecContext(ctx, `SELECT 1 FROM billing_year_neighbors
		WHERE billing_year_id=$1 AND neighbor_id=$2 FOR UPDATE`, yearID, neighborID); err != nil {
		t.Fatalf("hold account: %v", err)
	}

	type invoiceResult struct {
		invoice models.Invoice
		err     error
	}
	invoiceDone := make(chan invoiceResult, 1)
	go func() {
		iv, err := st.IssueInvoice(ctx, yearID, neighborID, 2026,
			time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC))
		invoiceDone <- invoiceResult{invoice: iv, err: err}
	}()
	waitForDatabaseBlock(t, ctx, pool, holderPID)

	type recalcResult struct {
		updated int
		err     error
	}
	recalcDone := make(chan recalcResult, 1)
	go func() {
		updated, _, _, err := st.ApplyRecalc(ctx, yearID, &neighborID)
		recalcDone <- recalcResult{updated: updated, err: err}
	}()
	waitForLockWaiters(t, ctx, pool, holderPID, 2)
	if err := holder.Commit(); err != nil {
		t.Fatalf("release holder: %v", err)
	}

	issued := <-invoiceDone
	if issued.err != nil {
		t.Fatalf("issue: %v", issued.err)
	}
	recalc := <-recalcDone
	if recalc.updated != 0 || !errors.Is(recalc.err, store.ErrInvoiceLocked) {
		t.Fatalf("recalc updated=%d err=%v, want 0/ErrInvoiceLocked", recalc.updated, recalc.err)
	}
	live, _, err := st.NeighborTotal(context.Background(), neighborID, yearID)
	if err != nil {
		t.Fatalf("live total: %v", err)
	}
	if !issued.invoice.Content.Net.Equal(live) || !live.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("frozen/live net = %s/%s, want 100/100", issued.invoice.Content.Net, live)
	}
}

func TestPaymentMutationAuditIsAtomicAndAttributable(t *testing.T) {
	st, pool, yearID, neighborID, _ := invoiceFixture(t, false)
	ctx := context.Background()
	userID, err := st.CreateUser(ctx, "payment-reviewer", "review-password-long-enough", models.RoleEditor)
	if err != nil {
		t.Fatalf("create actor: %v", err)
	}
	ctx = store.WithAuditActor(ctx, store.AuditActor{UserID: &userID, Username: "payment-reviewer", IP: "192.0.2.10"})
	firstDay := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := st.AddPayment(ctx, yearID, neighborID, decimal.NewFromInt(40), firstDay, "private note", "cash"); err != nil {
		t.Fatalf("add payment: %v", err)
	}
	payments, err := st.ListPayments(ctx, yearID, neighborID)
	if err != nil || len(payments) != 1 {
		t.Fatalf("list payments: len=%d err=%v", len(payments), err)
	}
	paymentID := payments[0].ID
	secondDay := firstDay.AddDate(0, 0, 1)
	if changed, err := st.UpdatePayment(ctx, paymentID, decimal.NewFromInt(40), secondDay, "changed private note", "bank"); err != nil || !changed {
		t.Fatalf("update payment: changed=%v err=%v", changed, err)
	}
	var entityID, detail string
	if err := pool.QueryRowContext(ctx, `SELECT entity_id, detail FROM audit_log
		WHERE action='payment_update' AND user_id=$1 ORDER BY id DESC LIMIT 1`, userID).Scan(&entityID, &detail); err != nil {
		t.Fatalf("load payment audit: %v", err)
	}
	if entityID != decimal.NewFromInt(paymentID).String() ||
		!strings.Contains(detail, "paid_on=2026-06-01") || !strings.Contains(detail, "paid_on=2026-06-02") ||
		!strings.Contains(detail, `method="cash"`) || !strings.Contains(detail, `method="bank"`) {
		t.Fatalf("incomplete payment audit: entity=%q detail=%q", entityID, detail)
	}
	if strings.Contains(detail, "private note") {
		t.Fatalf("payment audit retained unjustified free text: %q", detail)
	}

	invalidUserID := int64(1<<62 - 1)
	badCtx := store.WithAuditActor(ctx, store.AuditActor{UserID: &invalidUserID, Username: "missing"})
	if err := st.AddPayment(badCtx, yearID, neighborID, decimal.NewFromInt(5), secondDay, "", "cash"); err == nil {
		t.Fatal("payment with rejected audit unexpectedly committed")
	}
	payments, err = st.ListPayments(ctx, yearID, neighborID)
	if err != nil {
		t.Fatalf("list after rejected audit: %v", err)
	}
	if len(payments) != 1 {
		t.Fatalf("rejected audit left %d payments, want original one only", len(payments))
	}
}
