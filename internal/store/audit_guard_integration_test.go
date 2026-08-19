package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestAuditLogAppendOnlyGuardIntegration verifies the database-level tamper guard
// (migration 0036): an audit row cannot be UPDATEd at all, and cannot be DELETEd
// without the treckrr.allow_audit_prune opt-in — while the legitimate retention
// purge, which sets that flag, still works. Runs only when TEST_DATABASE_URL is set.
func TestAuditLogAppendOnlyGuardIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	const marker = "guard_test_marker"
	// Teardown deletes through the opt-in flag (the guard blocks a bare DELETE).
	cleanup := func() {
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			t.Errorf("cleanup begin: %v", err)
			return
		}
		defer func() { _ = tx.Rollback() }()
		if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_prune = 'on'`); err != nil {
			t.Errorf("cleanup set flag: %v", err)
			return
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE detail=$1`, marker); err != nil {
			t.Errorf("cleanup delete: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("cleanup commit: %v", err)
		}
	}
	cleanup()
	defer cleanup()

	if err := st.AddAudit(ctx, nil, "tester", "login", "auth", "", marker, "10.0.0.1"); err != nil {
		t.Fatalf("seed row: %v", err)
	}

	// 1. A direct UPDATE is always rejected — the trail is immutable.
	if _, err := pool.ExecContext(ctx,
		`UPDATE audit_log SET detail='tampered' WHERE detail=$1`, marker); err == nil {
		t.Fatal("UPDATE on audit_log succeeded; append-only guard did not block it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE rejected but with unexpected error: %v", err)
	}

	// 2. A DELETE without the opt-in flag is rejected.
	if _, err := pool.ExecContext(ctx,
		`DELETE FROM audit_log WHERE detail=$1`, marker); err == nil {
		t.Fatal("bare DELETE on audit_log succeeded; append-only guard did not block it")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE rejected but with unexpected error: %v", err)
	}

	// 3. The row is still there after both blocked mutations.
	var n int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE detail=$1`, marker).Scan(&n); err != nil {
		t.Fatalf("count after blocked mutations: %v", err)
	}
	if n != 1 {
		t.Fatalf("row count after blocked UPDATE/DELETE = %d, want 1 (nothing changed)", n)
	}

	// 4. A DELETE that opts in via SET LOCAL (as PurgeAuditLog does) succeeds.
	tx, err := pool.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin opt-in tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `SET LOCAL treckrr.allow_audit_prune = 'on'`); err != nil {
		_ = tx.Rollback()
		t.Fatalf("set opt-in flag: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM audit_log WHERE detail=$1`, marker); err != nil {
		_ = tx.Rollback()
		t.Fatalf("opt-in DELETE failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit opt-in delete: %v", err)
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM audit_log WHERE detail=$1`, marker).Scan(&n); err != nil {
		t.Fatalf("count after opt-in delete: %v", err)
	}
	if n != 0 {
		t.Fatalf("row count after opt-in DELETE = %d, want 0", n)
	}
}
