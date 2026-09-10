//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/store"
)

func TestDeletePersonConcurrentBooking(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	personID, err := st.CreatePerson(ctx, "Concurrent helper", dec("30"), "")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	var entryID int64
	var backendPID int
	if err := tx.QueryRowContext(ctx, `INSERT INTO entries
		(neighbor_id, billing_year_id, entry_date, tractor_label, load_label, hours,
		 hourly_rate, cost, person_id) VALUES ($1,$2,current_date,'','',1,30,30,$3)
		RETURNING id, pg_backend_pid()`, neighborID, yearID, personID).Scan(&entryID, &backendPID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- st.DeletePerson(ctx, personID) }()
	waitForDatabaseBlock(t, ctx, pool, backendPID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, store.ErrHasHistory) {
		t.Fatalf("concurrent delete = %v, want ErrHasHistory", err)
	}
	var got *int64
	if err := pool.QueryRowContext(ctx, `SELECT person_id FROM entries WHERE id=$1`, entryID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != personID {
		t.Fatalf("booking lost helper attribution: %v, want %d", got, personID)
	}
	if err := st.DeletePerson(ctx, personID+1000); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing person = %v, want ErrNotFound", err)
	}
	unusedID, err := st.CreatePerson(ctx, "Unused helper", dec("30"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePerson(ctx, unusedID); err != nil {
		t.Fatalf("unused helper must remain deletable: %v", err)
	}
}
