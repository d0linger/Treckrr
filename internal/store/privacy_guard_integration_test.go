//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

func TestAnonymizedPersonalDataGuardsIntegration(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx := context.Background()
	entryID, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: day(2026, 1, 1),
		TaskLabel: "Personal task", Note: "Personal note", Cost: dec("10"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ledgerID, err := st.AddNeighborLedger(ctx, yearID, neighborID, dec("1"), "Personal ledger", day(2026, 1, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntryVoided(ctx, entryID, true, "Personal entry reason"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLedgerVoided(ctx, ledgerID, true, "Personal ledger reason"); err != nil {
		t.Fatal(err)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatal(err)
	}
	entry, err := st.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Note != "" || entry.TaskLabel != "" || entry.VoidReason != "" || !entry.Cost.Equal(dec("10")) {
		t.Fatalf("live erasure lost an amount or retained entry text: %+v", entry)
	}
	_, _, ledger, err := st.GetLedgerEntry(ctx, ledgerID)
	if err != nil {
		t.Fatal(err)
	}
	if ledger.Description != "" || ledger.VoidReason != "" || !ledger.Amount.Equal(dec("1")) {
		t.Fatalf("live erasure lost an amount or retained ledger text: %+v", ledger)
	}
	for _, tc := range []struct {
		name  string
		write func() error
	}{
		{name: "photo", write: func() error {
			_, err := st.AddEntryPhoto(ctx, entryID, []byte("personal photo"), "image/jpeg")
			return err
		}},
		{name: "installment", write: func() error {
			_, err := st.AddInstallment(ctx, yearID, neighborID, dec("1"), day(2026, 1, 2), "personal plan")
			return err
		}},
		{name: "mail", write: func() error {
			return st.EnqueueMail(ctx, store.OutboxMail{
				Kind: "beleg", NeighborID: neighborID, Recipient: "personal@example.invalid", Body: "personal mail",
			})
		}},
		{name: "share", write: func() error {
			_, err := st.CreateBelegShare(ctx, "privacy-test-share", neighborID, yearID, time.Now().Add(time.Hour), "test")
			return err
		}},
		{name: "recurring", write: func() error {
			return st.CreateRecurring(ctx, entryID, neighborID,
				models.RecurTemplate{Note: "personal recurring"}, "weekly", day(2026, 1, 2))
		}},
		{name: "reactivate", write: func() error { return st.SetNeighborArchived(ctx, neighborID, false) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.write(); !errors.Is(err, store.ErrNeighborAnonymized) {
				t.Fatalf("write after anonymization = %v, want ErrNeighborAnonymized", err)
			}
		})
	}
	// Repair data repopulated by a legacy version rather than silently treating
	// every repeated erasure request as a no-op.
	if _, err := pool.ExecContext(ctx,
		`UPDATE entries SET note='legacy note', void_reason='legacy reason' WHERE id=$1`, entryID); err != nil {
		t.Fatal(err)
	}
	if err := st.AnonymizeNeighbor(ctx, neighborID); err != nil {
		t.Fatal(err)
	}
	entry, err = st.GetEntry(ctx, entryID)
	if err != nil || entry.Note != "" || entry.VoidReason != "" {
		t.Fatalf("repeated erasure retained legacy text: %+v (%v)", entry, err)
	}
	neighbor, err := st.GetNeighbor(ctx, neighborID)
	if err != nil || !neighbor.Archived || !neighbor.Anonymized {
		t.Fatalf("neighbor was reactivated: %+v (%v)", neighbor, err)
	}
}

func TestPersonalDataWriteWaitsForErasureStateIntegration(t *testing.T) {
	st, pool, yearID, neighborID := scratchBookingFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	entryID, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: neighborID, BillingYearID: yearID, Date: day(2026, 1, 1), Cost: dec("10"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var blockerPID int
	if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	// Hold the erasure state uncommitted. A normal FK KEY SHARE check does not
	// conflict with this update, so the live-state guard itself must block.
	if _, err := tx.ExecContext(ctx, `UPDATE neighbors SET anonymized=true WHERE id=$1`, neighborID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer close(done)
		_, err := st.AddEntryPhoto(ctx, entryID, []byte("racing photo"), "image/jpeg")
		done <- err
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitForDatabaseBlock(t, ctx, pool, blockerPID)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, store.ErrNeighborAnonymized) {
		t.Fatalf("racing photo = %v, want ErrNeighborAnonymized", err)
	}
	photos, err := st.ListEntryPhotos(ctx, entryID)
	if err != nil || len(photos) != 0 {
		t.Fatalf("erased account gained photos: %v (%v)", photos, err)
	}
}
