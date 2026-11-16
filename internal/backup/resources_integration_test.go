//go:build integration

package backup_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/db"
)

// TestWorkStopsWhenClusterLeaseIsLostIntegration pins the cross-process lease:
// the advisory lock dies with its session, and another process may take it
// at once. The admitted work must then be canceled and refuse nested work,
// instead of running beside the new holder (Codex audit 2026-11,
// backup-work-lease-loss-overlap).
func TestWorkStopsWhenClusterLeaseIsLostIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	poolA, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer poolA.Close()
	poolB, err := db.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer poolB.Close()
	serviceA := backup.New(backup.Options{}, poolA)
	serviceB := backup.New(backup.Options{}, poolB)

	workCtx, releaseA, err := serviceA.AcquireWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	if _, _, err := serviceB.AcquireWork(t.Context()); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("second process while the lease is held = %v, want ErrBusy", err)
	}
	const leaseKey int64 = 472019260911
	var pid int
	if err := poolB.QueryRowContext(t.Context(), `
		SELECT pid FROM pg_locks
		 WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted
		   AND database=(SELECT oid FROM pg_database WHERE datname=current_database())
		   AND classid=($1::bigint >> 32)::oid AND objid=($1::bigint & 4294967295)::oid
		 LIMIT 1`, leaseKey).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := poolB.QueryRowContext(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate lease backend: terminated=%v err=%v", terminated, err)
	}

	select {
	case <-workCtx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("admitted work kept running after its lease session ended")
	}
	if cause := context.Cause(workCtx); !errors.Is(cause, backup.ErrWorkLeaseLost) {
		t.Fatalf("cancel cause = %v, want ErrWorkLeaseLost", cause)
	}
	if _, _, err := serviceA.AcquireWork(workCtx); err == nil {
		t.Fatal("nested work was admitted on a lost lease")
	}
	releaseA()
	_, releaseB, err := serviceB.AcquireWork(t.Context())
	if err != nil {
		t.Fatalf("second process after the lost lease: %v", err)
	}
	releaseB()
	_, releaseAgain, err := serviceA.AcquireWork(t.Context())
	if err != nil {
		t.Fatalf("first process after release: %v", err)
	}
	releaseAgain()
}
