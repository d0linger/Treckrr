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

// TestSimilarEntryExistsIntegration proves the duplicate signal: a same-day
// booking with the same named task matches; a different date/task/empty task or
// a voided entry does not. Runs only when TEST_DATABASE_URL is set.
func TestSimilarEntryExistsIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// As a Cleanup, not a defer: the purge below is also a Cleanup, and
	// Cleanups run LIFO after all defers — a deferred Close would slam the
	// pool shut before the purge gets to run.
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	pid := os.Getpid()
	// Purge BEFORE seeding, and check every error. Both were missing: the
	// fixture year and neighbor name are UNIQUE and derived from the pid, which
	// container runtimes reuse — so a second run with the same pid hit the
	// unique constraints, the discarded errors left baseID/yearID/nid at 0, and
	// the first insert failed as a foreign-key violation on id 0. That is what
	// made this suite intermittently red for reasons unrelated to the code.
	f := fixtures{Years: []int{4200 + pid%1000}, NeighborNames: []string{fmt.Sprintf("Dup Nachbar %d", pid)}}
	purgeFixtures(t, ctx, pool, f)
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f) })

	baseID, err := st.CreateEmptyBase(ctx, 4200+pid%1000, "Dup-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 4200+pid%1000, baseID, "Dup-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, fmt.Sprintf("Dup Nachbar %d", pid), "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("membership: %v", err)
	}
	day := time.Date(2099, 5, 9, 0, 0, 0, 0, time.UTC)
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: day, TaskLabel: "Mähen",
		Unit: "h", Hours: decimal.RequireFromString("2"), HourlyRate: decimal.RequireFromString("40"),
		Cost: decimal.RequireFromString("80"),
	}, nil); err != nil {
		t.Fatalf("entry: %v", err)
	}

	check := func(date time.Time, task string, want bool) {
		got, err := st.SimilarEntryExists(ctx, nid, yearID, date, task)
		if err != nil {
			t.Fatalf("SimilarEntryExists: %v", err)
		}
		if got != want {
			t.Errorf("SimilarEntryExists(%s, %q) = %v, want %v", date.Format("2006-01-02"), task, got, want)
		}
	}
	check(day, "Mähen", true)                   // exact same-day + task
	check(day, "Pflügen", false)                // different task
	check(day.AddDate(0, 0, 1), "Mähen", false) // different day
	check(day, "", false)                       // empty task never matches
}
