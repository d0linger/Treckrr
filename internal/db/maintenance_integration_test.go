//go:build integration

package db

import (
	"errors"
	"os"
	"testing"
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
