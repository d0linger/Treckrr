package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestEntryIdempotencyIntegration proves that replaying an offline booking with
// the same idempotency key is a safe no-op: the second CreateEntry returns id 0
// and no duplicate row is inserted, while entries WITHOUT a key are never
// deduplicated. Runs only when TEST_DATABASE_URL is set.
func TestEntryIdempotencyIntegration(t *testing.T) {
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
	baseID, _ := st.CreateEmptyBase(ctx, 4400+pid%1000, "Idem-Basis")
	yearID, _ := st.CreateBillingYear(ctx, 4400+pid%1000, baseID, "Idem-Jahr")
	nid, _ := st.CreateNeighbor(ctx, fmt.Sprintf("Idem Nachbar %d", pid), "")
	_ = st.AddNeighborToYear(ctx, yearID, nid)

	mk := func(key string) *models.Entry {
		return &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Offline",
			Unit: "h", Hours: decimal.RequireFromString("1"), HourlyRate: decimal.RequireFromString("40"),
			Cost: decimal.RequireFromString("40"), IdempotencyKey: key,
		}
	}
	key := fmt.Sprintf("offline-%d", pid)

	id1, err := st.CreateEntry(ctx, mk(key), nil)
	if err != nil || id1 == 0 {
		t.Fatalf("first create: id=%d err=%v", id1, err)
	}
	// Replay with the same key → no-op, id 0, no duplicate.
	id2, err := st.CreateEntry(ctx, mk(key), nil)
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if id2 != 0 {
		t.Errorf("replay should return id 0 (no-op), got %d", id2)
	}

	count := func() int {
		es, err := st.ListEntries(ctx, nid, yearID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return len(es)
	}
	if c := count(); c != 1 {
		t.Fatalf("after replay, entries = %d, want 1", c)
	}

	// Two keyless bookings are NOT deduplicated.
	if _, err := st.CreateEntry(ctx, mk(""), nil); err != nil {
		t.Fatalf("keyless 1: %v", err)
	}
	if _, err := st.CreateEntry(ctx, mk(""), nil); err != nil {
		t.Fatalf("keyless 2: %v", err)
	}
	if c := count(); c != 3 {
		t.Errorf("keyless bookings must not dedupe: entries = %d, want 3", c)
	}
}
