//go:build integration

package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// TestApplicationLeaseMonitorPausesDuringExclusiveRestore verifies heartbeats
// stop while this process holds the exclusive restore lock, so load from the
// restore itself cannot end the session, and resume afterwards.
func TestApplicationLeaseMonitorPausesDuringExclusiveRestore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	lease, err := AcquireApplicationLease(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	release, err := lease.Exclusive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := pool.QueryRowContext(t.Context(), `
		SELECT pid FROM pg_locks
		 WHERE locktype='advisory' AND mode='ExclusiveLock' AND granted
		   AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
		   AND classid = ($1::bigint >> 32)::oid AND objid = ($1::bigint & 4294967295)::oid
		 LIMIT 1`, int64(applicationLeaseKey)).Scan(&pid); err != nil {
		_ = release()
		t.Skipf("cannot locate lease backend: %v", err)
	}
	// Break the session while the restore holds the lock: a paused monitor
	// must not notice until the exclusive section ends.
	var terminated bool
	if err := pool.QueryRowContext(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil || !terminated {
		_ = release()
		t.Skipf("cannot terminate lease backend: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	if err := lease.monitor(ctx, 10*time.Millisecond, time.Second, 3); err != nil {
		cancel()
		t.Fatalf("monitor probed during the exclusive restore: %v", err)
	}
	cancel()
	_ = release() // the session is gone; unlocking fails but frees l.mu
	ctx, cancel = context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := lease.monitor(ctx, 10*time.Millisecond, time.Second, 3); !errors.Is(err, ErrApplicationLeaseLost) {
		t.Fatalf("monitor after restore = %v, want ErrApplicationLeaseLost", err)
	}
}
