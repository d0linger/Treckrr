package store_test

import (
	"context"
	"os"
	"testing"

	"treckrr/internal/db"
	"treckrr/internal/store"
)

// TestResetPoolKeepsPoolUsable proves the *sql.DB equivalent of pgxpool.Reset():
// after discarding the pooled connections the pool still serves queries (it
// reopens fresh connections on demand).
func TestResetPoolKeepsPoolUsable(t *testing.T) {
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

	var n int
	if err := pool.QueryRowContext(ctx, `SELECT 1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pre-reset query: n=%d err=%v", n, err)
	}
	db.ResetPool(pool)
	if err := pool.QueryRowContext(ctx, `SELECT 1`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("post-reset query must still work: n=%d err=%v", n, err)
	}
}

// TestReconcileAfterRestoreReappliesMigration mirrors the real hazard: restoring a
// backup that predates migration 0024 leaves the schema a version behind (its
// columns and schema_migrations row are the backup's), so every query touching the
// new columns fails. The post-restore reconcile must bring it forward in one shot,
// so no container restart is needed — the fix ported from Parkrr.
func TestReconcileAfterRestoreReappliesMigration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Close last: t.Cleanup is LIFO, so register the pool close first (runs after
	// the re-migrate cleanup below, which still needs an open pool). A plain
	// `defer pool.Close()` would run before any t.Cleanup and close it too early.
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	// The test DB is shared; make sure it is left fully migrated even if an
	// assertion below fails after we deliberately drop part of the schema.
	t.Cleanup(func() {
		if err := db.Migrate(context.Background(), pool); err != nil {
			t.Errorf("cleanup: re-migrate shared test DB: %v", err)
		}
	})

	// Simulate restoring a pre-0024 dump: clear invoices (so the re-created partial
	// unique index cannot trip over rows that all default back to issued), drop the
	// columns/index 0024 added and remove its migrations row — exactly the state a
	// restored older backup leaves behind.
	if _, err := pool.ExecContext(ctx, `DELETE FROM invoices`); err != nil {
		t.Fatalf("simulate old backup (clear invoices): %v", err)
	}
	if _, err := pool.ExecContext(ctx, `ALTER TABLE invoices
		DROP COLUMN IF EXISTS net, DROP COLUMN IF EXISTS vat_rate, DROP COLUMN IF EXISTS vat_amount,
		DROP COLUMN IF EXISTS gross, DROP COLUMN IF EXISTS show_vat, DROP COLUMN IF EXISTS tax_mode,
		DROP COLUMN IF EXISTS tax_note, DROP COLUMN IF EXISTS service_from, DROP COLUMN IF EXISTS service_to,
		DROP COLUMN IF EXISTS issuer, DROP COLUMN IF EXISTS recipient, DROP COLUMN IF EXISTS lines,
		DROP COLUMN IF EXISTS content_hash, DROP COLUMN IF EXISTS kind, DROP COLUMN IF EXISTS status,
		DROP COLUMN IF EXISTS references_invoice_id, DROP COLUMN IF EXISTS payment_reference`); err != nil {
		t.Fatalf("simulate old backup (drop invoice columns): %v", err)
	}
	if _, err := pool.ExecContext(ctx, `ALTER TABLE company DROP COLUMN IF EXISTS iban`); err != nil {
		t.Fatalf("simulate old backup (drop company.iban): %v", err)
	}
	if _, err := pool.ExecContext(ctx, `DELETE FROM schema_migrations WHERE name='0024_invoice_snapshot.sql'`); err != nil {
		t.Fatalf("simulate old backup (migrations row): %v", err)
	}
	// Precondition: a query using a 0024 column now fails — the "stale schema" state.
	if _, err := pool.ExecContext(ctx, `SELECT net FROM invoices LIMIT 1`); err == nil {
		t.Fatalf("precondition: invoices.net should be gone after the simulated old restore")
	}

	// The reconcile that runs at the end of a restore must fix it without a restart.
	if err := st.ReconcileAfterRestore(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// 0024 is recorded again, and the previously-failing queries run.
	var has bool
	if err := pool.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='0024_invoice_snapshot.sql')`).Scan(&has); err != nil || !has {
		t.Errorf("migration 0024 must be re-applied after restore (has=%v err=%v)", has, err)
	}
	if _, err := pool.ExecContext(ctx, `SELECT net, kind, status, payment_reference FROM invoices LIMIT 1`); err != nil {
		t.Errorf("invoice snapshot columns must exist after reconcile: %v", err)
	}
	if _, err := pool.ExecContext(ctx, `SELECT iban FROM company LIMIT 1`); err != nil {
		t.Errorf("company.iban must exist after reconcile: %v", err)
	}
}
