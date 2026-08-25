// Package db provides the PostgreSQL connection pool and schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Pool sizing, shared by Connect and ResetPool so a reset restores the same caps.
const (
	maxOpenConns    = 10
	maxIdleConns    = 5
	connMaxLifetime = time.Hour
)

// Server-side guards applied to every pooled connection. The pool is small (10),
// so a single query that never returns takes a tenth of the app's capacity with
// it — and a handful of them stop the application entirely while the process
// still looks healthy. Postgres enforces these regardless of what the Go side
// does with contexts, which is the point: a caller that forgets a deadline, or a
// context that outlives its request, is still bounded.
//
// statementTimeout is generous relative to real queries (the slowest report is
// well under a second) and only fires on something genuinely stuck.
// idleInTransactionTimeout catches the worse case: a transaction left open holds
// its connection AND blocks vacuum on the rows it touched.
const (
	statementTimeout         = "30s"
	idleInTransactionTimeout = "60s"
)

// withTimeouts adds the server-side timeouts unless the operator already set
// them, for BOTH DSN forms libpq accepts: the URL form used everywhere in this
// repository, and the keyword/value form ("host=… user=…") an operator may
// legitimately supply. Handling only the first left the hardening silently
// inactive for the second, which is the worst kind of default — one that looks
// applied and is not.
//
// Anything it cannot parse is passed through untouched rather than rejected: a
// hardening default must never stop a working deployment from booting.
func withTimeouts(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" {
		if u.Scheme != "postgres" && u.Scheme != "postgresql" {
			return dsn // not a Postgres URL; leave it alone
		}
		q := u.Query()
		if q.Get("statement_timeout") == "" {
			q.Set("statement_timeout", statementTimeout)
		}
		if q.Get("idle_in_transaction_session_timeout") == "" {
			q.Set("idle_in_transaction_session_timeout", idleInTransactionTimeout)
		}
		u.RawQuery = q.Encode()
		return u.String()
	}

	// Keyword/value form. pgconn's own parser decides what is already set —
	// scanning the string by hand would mis-read quoted values. Unknown keys land
	// in RuntimeParams, which is exactly where these two belong, so appending them
	// produces a DSN the driver accepts unchanged.
	cfg, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return dsn
	}
	out := dsn
	if cfg.RuntimeParams["statement_timeout"] == "" {
		out += " statement_timeout=" + statementTimeout
	}
	if cfg.RuntimeParams["idle_in_transaction_session_timeout"] == "" {
		out += " idle_in_transaction_session_timeout=" + idleInTransactionTimeout
	}
	return out
}

// Connect opens a connection pool and waits until the database is reachable.
func Connect(ctx context.Context, dsn string) (*sql.DB, error) {
	pool, err := sql.Open("pgx", withTimeouts(dsn))
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	pool.SetMaxOpenConns(maxOpenConns)
	pool.SetMaxIdleConns(maxIdleConns)
	pool.SetConnMaxLifetime(connMaxLifetime)

	// Retry until Postgres accepts connections (it may still be starting up).
	deadline := time.Now().Add(60 * time.Second)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = pool.PingContext(pingCtx)
		cancel()
		if err == nil {
			return pool, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("database not reachable: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// ResetPool discards the pooled connections so none keeps a prepared statement
// bound to a table that a `pg_restore --clean` just dropped and recreated — the
// classic "cached plan must not change result type" after an in-place restore.
// database/sql has no Reset() like pgxpool: dropping the idle limit to zero closes
// every idle connection synchronously, and restoring it lets the pool reopen fresh
// connections (with an empty statement cache) on demand. Call it before re-running
// Migrate so the migration and all later queries run on clean connections.
func ResetPool(pool *sql.DB) {
	pool.SetMaxIdleConns(0)
	pool.SetMaxIdleConns(maxIdleConns)
}

// migrateLockKey is a fixed advisory-lock key that serializes Migrate across
// instances (any constant works as long as it's unique to this concern).
const migrateLockKey = 4711

// Migrate applies every embedded migration that has not yet run, in order.
// It runs under a session-level Postgres advisory lock so two instances booting
// together cannot both apply the same migration (a data-backfill migration could
// otherwise double-apply before its constraints exist). Other instances block on
// the lock, then find every migration already recorded and no-op.
func Migrate(ctx context.Context, pool *sql.DB) error {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire conn: %w", err)
	}
	defer conn.Close() // releases the session advisory lock even on error
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrateLockKey); err != nil {
		return fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, migrateLockKey) }()

	if _, err := pool.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var exists bool
		if err := pool.QueryRowContext(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name = $1)`, name,
		).Scan(&exists); err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		content, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := pool.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		// Migrations are exempt. A schema change (an index build, a constraint
		// validation over a large table) is precisely the long-running statement
		// the pool-wide timeout is there to kill, and killing it half-way would
		// leave the schema behind its recorded version. SET LOCAL reverts on
		// commit or rollback, so only this transaction is affected.
		if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = 0`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: lift statement timeout: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}
