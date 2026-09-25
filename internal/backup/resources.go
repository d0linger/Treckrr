package backup

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// ErrBusy rejects another large backup job before it allocates memory.
var ErrBusy = errors.New("another backup operation is running")

// backupWorkLeaseKey serializes memory-heavy backup, restore, mirror and key
// rotation work across every application process connected to this database.
const backupWorkLeaseKey int64 = 472019260911

type workKey struct{}
type workLease struct {
	service *Service
	active  atomic.Bool
}

// AcquireWork admits one memory-heavy operation. HTTP callers acquire before
// parsing and keep the lease until their response and multipart cleanup finish.
// Nested service calls reuse the lease; do not launch parallel work with it.
func (s *Service) AcquireWork(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	if lease, ok := ctx.Value(workKey{}).(*workLease); ok && lease.service == s && lease.active.Load() {
		return ctx, func() {}, nil
	}
	select {
	case s.workSem <- struct{}{}:
	default:
		return ctx, nil, ErrBusy
	}
	clusterRelease, err := s.acquireClusterWork(ctx)
	if err != nil {
		<-s.workSem
		return ctx, nil, err
	}
	lease := &workLease{service: s}
	lease.active.Store(true)
	var once sync.Once
	release := func() {
		once.Do(func() {
			lease.active.Store(false)
			clusterRelease()
			<-s.workSem
		})
	}
	return context.WithValue(ctx, workKey{}, lease), release, nil
}

// acquireClusterWork takes the cross-process advisory lease for memory-heavy
// backup operations and returns an idempotent caller-owned release function.
func (s *Service) acquireClusterWork(ctx context.Context) (func(), error) {
	if s.db == nil {
		return func() {}, nil
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, backupWorkLeaseKey).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !acquired {
		_ = conn.Close()
		return nil, ErrBusy
	}
	return func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(releaseCtx, `SELECT pg_advisory_unlock($1)`, backupWorkLeaseKey); err != nil {
			slog.Error("backup: cluster work lease release failed", "err", err)
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
		_ = conn.Close()
	}, nil
}

// maxBytes returns the bounded in-memory archive allowance.
func (s *Service) maxBytes() int64 {
	if s.opt.MaxBytes > 16<<30 {
		return 16 << 30 // avoid overflow in max+1 readers, even for internal callers
	}
	if s.opt.MaxBytes > 0 {
		return s.opt.MaxBytes
	}
	return maxS3ObjectBytes
}

// readArchive reads at most one configured archive budget plus a detection byte.
func (s *Service) readArchive(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, s.maxBytes()+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > s.maxBytes() {
		return nil, fmt.Errorf("archive exceeds %d bytes; use a provisioned offline restore for larger files", s.maxBytes())
	}
	return data, nil
}

// readFile opens an operator-selected offline archive under the memory budget.
func (s *Service) readFile(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied offline CLI path
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.readArchive(f)
}

// limitedWriter rejects writes that would exceed its remaining byte budget.
type limitedWriter struct {
	w         io.Writer
	remaining int64
}

// Write implements io.Writer while enforcing the remaining byte budget.
func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("backup exceeds configured memory budget")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}
