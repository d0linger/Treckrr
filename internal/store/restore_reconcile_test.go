package store_test

import (
	"context"
	"database/sql"
	neturl "net/url"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
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
//
// It runs against its OWN throwaway database, not the shared one. The previous
// version dropped 0024's seventeen columns on the SHARED invoices table and let
// the reconcile re-add them — but Postgres counts dropped columns against the
// 1600-column table limit FOREVER, so every suite run burned seventeen slots
// until, after enough runs, migration 0024 could not apply at all and the whole
// suite collapsed (SQLSTATE 54011). It also ran DELETE FROM invoices mid-suite,
// silently eating other tests' fixtures. A scratch database resets both problems
// on every run and needs no cleanup discipline at all.
func TestReconcileAfterRestoreReappliesMigration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()

	// Maintenance connection to create/drop the scratch database.
	base, err := neturl.Parse(url)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	admin := *base
	admin.Path = "/postgres"
	adminPool, err := sql.Open("pgx", admin.String())
	if err != nil {
		t.Fatalf("admin connect: %v", err)
	}
	t.Cleanup(func() { _ = adminPool.Close() })
	const scratch = "treckrr_reconcile_test"
	drop := func() {
		_, _ = adminPool.ExecContext(ctx, `DROP DATABASE IF EXISTS `+scratch+` WITH (FORCE)`)
	}
	drop() // a crashed previous run leaves it behind
	if _, err := adminPool.ExecContext(ctx, `CREATE DATABASE `+scratch); err != nil {
		t.Fatalf("create scratch db: %v", err)
	}
	t.Cleanup(drop)

	scratchURL := *base
	scratchURL.Path = "/" + scratch
	pool, err := db.Connect(ctx, scratchURL.String())
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate scratch: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	// Simulate restoring a pre-0024 dump: drop the columns/index 0024 added and
	// remove its migrations row — exactly the state a restored older backup
	// leaves behind. The database is ours alone, so no shared fixture can be hit.
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
