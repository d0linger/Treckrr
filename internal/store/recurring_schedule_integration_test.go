//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestRecurringScheduleBoundsIntegration proves the end date is inclusive,
// completion deactivates the rule, and skipping records an exception without
// creating or modifying a booking.
func TestRecurringScheduleBoundsIntegration(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	st, pool, _, neighborID, sourceID := invoiceFixtureYear(t, false, today.Year())
	ctx := context.Background()
	tmpl := models.RecurTemplate{
		TaskLabel: "bounded recurring", Unit: "Pauschale",
		Quantity: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10), Cost: decimal.NewFromInt(10),
	}
	if err := st.CreateRecurringUntil(ctx, sourceID, neighborID, tmpl, "weekly", today, &today); err != nil {
		t.Fatal(err)
	}
	created, err := st.RunDueRecurring(ctx)
	if err != nil || created != 1 {
		t.Fatalf("bounded run: created=%d err=%v", created, err)
	}
	var ruleID int64
	var active bool
	var next time.Time
	if err := pool.QueryRowContext(ctx, `
		SELECT id, active, next_run FROM recurring_entries WHERE neighbor_id=$1`, neighborID).
		Scan(&ruleID, &active, &next); err != nil {
		t.Fatal(err)
	}
	if active || !next.After(today) {
		t.Fatalf("completed rule active=%v next=%s", active, next)
	}
	if _, err := st.ToggleRecurring(ctx, ruleID); !errors.Is(err, store.ErrRecurringEnded) {
		t.Fatalf("ended rule reactivated: %v", err)
	}

	start := today.AddDate(0, 0, 7)
	end := start
	if err := st.CreateRecurringUntil(ctx, sourceID, neighborID, tmpl, "weekly", start, &end); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT id FROM recurring_entries WHERE neighbor_id=$1 ORDER BY id DESC LIMIT 1`, neighborID).Scan(&ruleID); err != nil {
		t.Fatal(err)
	}
	result, err := st.SkipNextRecurring(ctx, ruleID)
	if err != nil {
		t.Fatal(err)
	}
	// next_run is a PostgreSQL DATE and is scanned at UTC midnight, while start
	// uses the test host's local midnight. Compare the represented calendar day,
	// not two location-dependent instants.
	if result.Active || result.SkippedOn.Format("2006-01-02") != start.Format("2006-01-02") {
		t.Fatalf("skip result = %+v", result)
	}
	var exceptions, entries int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM recurring_exceptions WHERE recurring_id=$1`, ruleID).Scan(&exceptions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM entries WHERE neighbor_id=$1 AND task_label='bounded recurring'`, neighborID).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if exceptions != 1 || entries != 1 {
		t.Fatalf("exceptions=%d generated entries=%d, want 1/1", exceptions, entries)
	}
	before := start.AddDate(0, 0, -1)
	if err := st.UpdateRecurringSchedule(ctx, ruleID, "weekly", start, &before); !errors.Is(err, store.ErrRecurringEndBeforeStart) {
		t.Fatalf("invalid schedule error = %v", err)
	}
}
