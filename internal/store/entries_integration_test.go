//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// ON CONFLICT waits for the competing insert to commit; the following SELECT
// gets a new READ COMMITTED snapshot and can see that row. Do not suppress a
// missing-row error and accidentally accept an unlinked companion instead.
func TestCreateEntryPairWaitsForConcurrentReplay(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var mainID int64
	var backendPID int
	if err := tx.QueryRowContext(ctx, `INSERT INTO entries
		(neighbor_id, billing_year_id, entry_date, tractor_label, load_label,
		 hours, hourly_rate, cost, idempotency_key)
		VALUES ($1,$2,current_date,'','',2,40,80,'concurrent-replay')
		RETURNING id, pg_backend_pid()`, neighborID, yearID).Scan(&mainID, &backendPID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		mainID      int64
		companionID int64
		err         error
	}
	done := make(chan result, 1)
	go func() {
		machine, companion, err := st.CreateEntryPair(ctx, &models.Entry{
			NeighborID: neighborID, BillingYearID: yearID, Date: time.Now(),
			Hours: dec("2"), HourlyRate: dec("40"), Cost: dec("80"), IdempotencyKey: "concurrent-replay",
		}, nil, &models.Entry{
			NeighborID: neighborID, BillingYearID: yearID, Date: time.Now(),
			Unit: models.UnitMannstunde, Quantity: dec("2"), UnitPrice: dec("30"),
			Cost: dec("60"), IdempotencyKey: "concurrent-replay-p",
		})
		done <- result{mainID: machine, companionID: companion, err: err}
	}()
	waitForDatabaseBlock(t, ctx, pool, backendPID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || got.mainID != 0 || got.companionID == 0 {
		t.Fatalf("replay = %+v, want existing main and newly linked companion", got)
	}
	partner, err := st.LinkedPartnerID(ctx, mainID)
	if err != nil || partner != got.companionID {
		t.Fatalf("partner = %d, %v; want %d", partner, err, got.companionID)
	}
}
