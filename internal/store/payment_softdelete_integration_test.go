package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestPaymentSoftDeleteIntegration proves the undo path: a soft-deleted payment
// drops out of the sum immediately, restore brings it back, and purge removes it
// for good — verifying that every payment-sum query got the deleted_at filter.
func TestPaymentSoftDeleteIntegration(t *testing.T) {
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

	pid := os.Getpid()
	baseID, _ := st.CreateEmptyBase(ctx, 4300+pid%1000, "SD-Basis")
	yearID, _ := st.CreateBillingYear(ctx, 4300+pid%1000, baseID, "SD-Jahr")
	nid, _ := st.CreateNeighbor(ctx, fmt.Sprintf("SD Nachbar %d", pid), "")
	_ = st.AddNeighborToYear(ctx, yearID, nid)
	if err := st.AddPayment(ctx, yearID, nid, decimal.RequireFromString("100"), time.Now(), ""); err != nil {
		t.Fatalf("add payment: %v", err)
	}

	sum := func() string {
		s, err := st.NeighborPaymentSum(ctx, yearID, nid)
		if err != nil {
			t.Fatalf("sum: %v", err)
		}
		return s.String()
	}
	payID := func() int64 {
		ps, err := st.ListPayments(ctx, yearID, nid)
		if err != nil || len(ps) == 0 {
			t.Fatalf("list: %v (len %d)", err, len(ps))
		}
		return ps[0].ID
	}

	if got := sum(); got != "100" {
		t.Fatalf("initial sum = %s, want 100", got)
	}
	id := payID()

	// Soft-delete → excluded from the sum and the list.
	if err := st.DeletePayment(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := sum(); got != "0" {
		t.Errorf("sum after soft-delete = %s, want 0", got)
	}
	if ps, _ := st.ListPayments(ctx, yearID, nid); len(ps) != 0 {
		t.Errorf("list after soft-delete has %d rows, want 0", len(ps))
	}
	if n, _ := st.CountPaymentsForNeighborYear(ctx, yearID, nid); n != 0 {
		t.Errorf("count after soft-delete = %d, want 0", n)
	}

	// Restore → back in the sum.
	if err := st.RestorePayment(ctx, id); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := sum(); got != "100" {
		t.Errorf("sum after restore = %s, want 100", got)
	}

	// Delete again, then purge with a future cutoff → gone for good.
	if err := st.DeletePayment(ctx, id); err != nil {
		t.Fatalf("delete2: %v", err)
	}
	if err := st.PurgeDeletedPayments(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if err := st.RestorePayment(ctx, id); err != nil {
		t.Fatalf("restore after purge should be a no-op: %v", err)
	}
	if got := sum(); got != "0" {
		t.Errorf("sum after purge = %s, want 0 (row hard-deleted)", got)
	}
}
