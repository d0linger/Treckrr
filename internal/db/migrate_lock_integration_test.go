//go:build integration

package db

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMigrateWaitsLongerThanStatementTimeout reproduces a second replica booting
// while the first one still holds the migration lock: its wait must outlast the
// pool's statement_timeout (shortened to one second here) and end with a normal
// Migrate once the lock is released, while its own context still bounds it.
func TestMigrateWaitsLongerThanStatementTimeout(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pool, err := Connect(t.Context(), dsn+sep+"statement_timeout=1000")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var timeout string
	if err := pool.QueryRowContext(t.Context(), `SHOW statement_timeout`).Scan(&timeout); err != nil || timeout != "1s" {
		t.Fatalf("statement_timeout = %q (%v), want the shortened 1s", timeout, err)
	}

	holder, err := pool.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(t.Context(), `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(t.Context(), 1500*time.Millisecond)
	defer cancel()
	if err := Migrate(short, pool); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Migrate while locked with an expiring context = %v, want deadline exceeded", err)
	}

	done := make(chan error, 1)
	go func() { done <- Migrate(t.Context(), pool) }()
	time.Sleep(2500 * time.Millisecond) // well past the 1 s statement_timeout
	select {
	case err := <-done:
		t.Fatalf("Migrate returned while the lock was held: %v", err)
	default:
	}
	if _, err := holder.ExecContext(t.Context(), `SELECT pg_advisory_unlock($1)`, migrateLockKey); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Migrate after waiting: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Migrate did not take the released lock")
	}
}
