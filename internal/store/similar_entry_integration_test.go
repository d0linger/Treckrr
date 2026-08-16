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
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	pid := os.Getpid()
	baseID, _ := st.CreateEmptyBase(ctx, 4200+pid%1000, "Dup-Basis")
	yearID, _ := st.CreateBillingYear(ctx, 4200+pid%1000, baseID, "Dup-Jahr")
	nid, _ := st.CreateNeighbor(ctx, fmt.Sprintf("Dup Nachbar %d", pid), "")
	_ = st.AddNeighborToYear(ctx, yearID, nid)
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
