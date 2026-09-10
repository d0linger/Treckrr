package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"time"
)

// A database-scoped lock shared by serving instances, exclusive during restore.
// No schema or business data is changed by acquiring these advisory leases.
const applicationLeaseKey = 472019260910

// ErrApplicationRunning refuses exclusive recovery until other instances stop.
var ErrApplicationRunning = errors.New("another application or restore is using this database; stop all app instances first")

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
