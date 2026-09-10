//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/store"
)

func TestDeleteProtectsConcurrentDeliveryHistory(t *testing.T) {
	for _, parent := range []string{"neighbor", "year"} {
		for _, record := range []string{"pending mail", "failed mail", "sent mail", "dunning notice"} {
			t.Run(parent+"/"+record, func(t *testing.T) {
				st, pool, yearID, neighborID := scratchBookingFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				tx, err := pool.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = tx.Rollback() }()
				var backendPID int
				if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
					t.Fatal(err)
				}
				if record == "dunning notice" {
					_, err = tx.ExecContext(ctx, `INSERT INTO dunning_notices
						(billing_year_id, neighbor_id, invoice_number, stage, channel)
						VALUES ($1,$2,'review-invoice',1,'print')`, yearID, neighborID)
				} else {
					status := "pending"
					if record == "failed mail" {
						status = "failed"
					}
					if record == "sent mail" {
						status = "sent"
					}
					_, err = tx.ExecContext(ctx, `INSERT INTO mail_outbox
						(kind, neighbor_id, billing_year_id, recipient, subject, body,
						 att_name, att_type, att_data, status)
						VALUES ('beleg',$1,$2,'review@example.invalid','review','body','','',''::bytea,$3)`,
						neighborID, yearID, status)
				}
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() {
					if parent == "neighbor" {
						done <- st.DeleteNeighbor(ctx, neighborID)
						return
					}
					done <- st.DeleteBillingYear(ctx, yearID)
				}()
				waitForDatabaseBlock(t, ctx, pool, backendPID)
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, store.ErrHasHistory) {
					t.Fatalf("delete = %v, want ErrHasHistory", err)
				}
				var blockers store.DeleteBlockers
				if parent == "neighbor" {
					blockers, err = st.NeighborDeleteBlockers(ctx, neighborID)
				} else {
					blockers, err = st.YearDeleteBlockers(ctx, yearID)
				}
				if err != nil || !blockers.Any() {
					t.Fatalf("delivery history lost: blockers=%+v err=%v", blockers, err)
				}
				if record == "dunning notice" && blockers.DunningNotices != 1 {
					t.Fatalf("dunning blockers = %d, want 1", blockers.DunningNotices)
				}
				if record != "dunning notice" && blockers.Outbox != 1 {
					t.Fatalf("outbox blockers = %d, want 1", blockers.Outbox)
				}
			})
		}
	}
}
