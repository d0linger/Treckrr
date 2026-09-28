package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// A database-scoped lock shared by serving instances, exclusive during restore.
// No schema or business data is changed by acquiring these advisory leases.
const applicationLeaseKey = 472019260910

// ErrApplicationRunning refuses exclusive recovery until other instances stop.
var ErrApplicationRunning = errors.New("another application or restore is using this database; stop all app instances first")

// ErrApplicationLeaseLost means the dedicated PostgreSQL session that fenced
// this serving process from an offline restore is no longer usable. Callers
// must stop admitting traffic immediately; reconnecting the ordinary pool does
// not recreate a session-scoped advisory lock.
var ErrApplicationLeaseLost = errors.New("application maintenance lease lost")

// ApplicationLease coordinates participating binaries through one dedicated
// pool connection. It is not fencing for external writers or network partitions.
type ApplicationLease struct {
	conn   *sql.Conn
	mu     sync.Mutex
	shared bool
}

// AcquireApplicationLease registers a serving instance before migrations or jobs.
// Keep the returned lease until all request and background work has stopped.
func AcquireApplicationLease(ctx context.Context, pool *sql.DB) (*ApplicationLease, error) {
	return acquireApplicationLease(ctx, pool, true)
}

// AcquireOfflineRestoreLease refuses a CLI recovery while participating apps or
// another restore run. Older binaries and external writers must be stopped first.
func AcquireOfflineRestoreLease(ctx context.Context, pool *sql.DB) (*ApplicationLease, error) {
	return acquireApplicationLease(ctx, pool, false)
}

func acquireApplicationLease(ctx context.Context, pool *sql.DB, shared bool) (*ApplicationLease, error) {
	conn, err := pool.Conn(ctx)
	if err != nil {
		return nil, err
	}
	query := `SELECT pg_try_advisory_lock($1)`
	if shared {
		query = `SELECT pg_try_advisory_lock_shared($1)`
	}
	var ok bool
	if err := conn.QueryRowContext(ctx, query, applicationLeaseKey).Scan(&ok); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !ok {
		_ = conn.Close()
		return nil, ErrApplicationRunning
	}
	return &ApplicationLease{conn: conn, shared: shared}, nil
}

// Exclusive upgrades this serving instance's own lease, but never another
// instance's. The caller must first drain its requests/background workers.
// Call the returned release exactly once and keep maintenance enabled on error.
func (l *ApplicationLease) Exclusive(ctx context.Context) (func() error, error) {
	l.mu.Lock()
	var ok bool
	if err := l.conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, applicationLeaseKey).Scan(&ok); err != nil {
		l.mu.Unlock()
		return nil, err
	}
	if !ok {
		l.mu.Unlock()
		return nil, ErrApplicationRunning
	}
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := l.conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, applicationLeaseKey)
		l.mu.Unlock()
		return err
	}, nil
}

// Heartbeat tuning for Monitor. The timeout is deliberately independent of,
// and much longer than, the interval: with pgx a ping that hits its deadline
// closes the connection, which ends the session and drops the advisory lock.
// A one-second timeout turned every checkpoint or I/O stall (typically the
// app's own pg_dump or restore) into a full restart.
const (
	leasePingTimeout     = 15 * time.Second
	leaseMaxPingFailures = 3
)

// Monitor verifies that the dedicated session holding the application lease is
// still alive. It returns nil on normal context cancellation and a wrapped
// ErrApplicationLeaseLost once the lease is gone: immediately when the session
// is closed, otherwise after leaseMaxPingFailures consecutive failed heartbeats.
// A failed lease must never be silently reacquired: an offline restore may
// already hold the exclusive lock by then.
//
// While this process holds the exclusive restore lock (Exclusive), heartbeats
// pause: the restore itself owns the session and the database is under its
// heaviest load, so a probe could only hurt.
func (l *ApplicationLease) Monitor(ctx context.Context, interval time.Duration) error {
	return l.monitor(ctx, interval, leasePingTimeout, leaseMaxPingFailures)
}

func (l *ApplicationLease) monitor(ctx context.Context, interval, timeout time.Duration, maxFailures int) error {
	if interval <= 0 {
		interval = time.Second
	}
	if timeout <= 0 {
		timeout = leasePingTimeout
	}
	if maxFailures < 1 {
		maxFailures = 1
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !l.mu.TryLock() {
				continue // exclusive restore (or Close) in progress
			}
			checkCtx, cancel := context.WithTimeout(ctx, timeout)
			err := l.conn.PingContext(checkCtx)
			cancel()
			closed := err != nil && l.sessionClosed()
			l.mu.Unlock()
			if err == nil {
				failures = 0
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			failures++
			if closed || failures >= maxFailures {
				return fmt.Errorf("%w: %v", ErrApplicationLeaseLost, err)
			}
		}
	}
}

// sessionClosed reports whether the lease's database session is definitely
// gone. A closed session has released its advisory locks, so retrying the
// heartbeat cannot help. Callers hold l.mu.
func (l *ApplicationLease) sessionClosed() bool {
	err := l.conn.Raw(func(driverConn any) error {
		if c, ok := driverConn.(interface{ IsClosed() bool }); ok && c.IsClosed() {
			return driver.ErrBadConn
		}
		if c, ok := driverConn.(interface{ Conn() *pgx.Conn }); ok && c.Conn().IsClosed() {
			return driver.ErrBadConn
		}
		return nil
	})
	return err != nil
}

// Close releases the lease and discards its session rather than returning a
// potentially still-locked connection to the pool. It is safe to call repeatedly.
func (l *ApplicationLease) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	query := `SELECT pg_advisory_unlock($1)`
	if l.shared {
		query = `SELECT pg_advisory_unlock_shared($1)`
	}
	_, _ = l.conn.ExecContext(ctx, query, applicationLeaseKey)
	// Session locks must never return to the pool, even when unlocking failed.
	_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = l.conn.Close()
}
