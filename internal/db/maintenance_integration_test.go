//go:build integration

package db

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestApplicationLeaseCoordinatesRestore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	pool, err := Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	first, err := AcquireApplicationLease(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := AcquireApplicationLease(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireOfflineRestoreLease(t.Context(), pool); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("offline admitted while app active: %v", err)
	}
	if _, err := first.Exclusive(t.Context()); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("online admitted with second app: %v", err)
	}
	second.Close()
	release, err := first.Exclusive(t.Context())
	if err != nil {
		t.Fatalf("own shared lease upgrade: %v", err)
	}
	if _, err := AcquireApplicationLease(t.Context(), pool); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("app started during restore: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	first.Close()
	offline, err := AcquireOfflineRestoreLease(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireApplicationLease(t.Context(), pool); !errors.Is(err, ErrApplicationRunning) {
		t.Fatalf("app admitted while CLI restore active: %v", err)
	}
	offline.Close()
	last, err := AcquireApplicationLease(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	last.Close()
}

// TestApplicationLeaseMonitorDetectsSessionLoss terminates the lease backend and
// verifies the monitor fails closed.
func TestApplicationLeaseMonitorDetectsSessionLoss(t *testing.T) {
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
	var pid int
	if err := lease.conn.QueryRowContext(t.Context(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var terminated bool
	if err := pool.QueryRowContext(t.Context(), `SELECT pg_terminate_backend($1)`, pid).Scan(&terminated); err != nil {
		t.Skipf("cannot terminate lease backend: %v", err)
	}
	if !terminated {
		t.Skip("database refused to terminate lease backend")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := lease.Monitor(ctx, 10*time.Millisecond); !errors.Is(err, ErrApplicationLeaseLost) {
		t.Fatalf("monitor error = %v, want ErrApplicationLeaseLost", err)
	}
}
