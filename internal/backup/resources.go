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

// ErrWorkLeaseLost is the cancel cause of admitted work whose cross-process
// lease ended with its database session: another process may hold it now.
var ErrWorkLeaseLost = errors.New("backup work lease lost: its database session ended")

// backupWorkLeaseKey serializes memory-heavy backup, restore, mirror and key
// rotation work across every application process connected to this database.
const backupWorkLeaseKey int64 = 472019260911

// The advisory lock lives exactly as long as the session holding it. Admitted
// work pings that session at this interval; a failed ping means the lock is
// gone, so the work is canceled instead of running beside the next holder.
const (
	workLeaseProbeInterval = 5 * time.Second
	workLeaseProbeTimeout  = 5 * time.Second
)

type workKey struct{}
type workLease struct {
	service *Service
	active  atomic.Bool
}

// AcquireWork admits one memory-heavy operation. HTTP callers acquire before
// parsing and keep the lease until their response and multipart cleanup finish.
// Nested service calls reuse the lease; do not launch parallel work with it.
// The returned context is canceled with ErrWorkLeaseLost when the
// cross-process lease is lost, and nested calls on it then fail closed.
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
	lease := &workLease{service: s}
	lease.active.Store(true)
	workCtx, cancel := context.WithCancelCause(ctx)
	clusterRelease, err := s.acquireClusterWork(ctx, func() {
		lease.active.Store(false)
		cancel(ErrWorkLeaseLost)
	})
	if err != nil {
		cancel(nil)
		<-s.workSem
		return ctx, nil, err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			lease.active.Store(false)
			clusterRelease()
			cancel(nil)
			<-s.workSem
		})
	}
	return context.WithValue(workCtx, workKey{}, lease), release, nil
}

// acquireClusterWork takes the cross-process advisory lease for memory-heavy
// backup operations and returns a release function the caller runs exactly
// once. While held, the lease's session is probed; lost runs once if it ends.
func (s *Service) acquireClusterWork(ctx context.Context, lost func()) (func(), error) {
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
	stop, stopped := make(chan struct{}), make(chan struct{})
	var dead atomic.Bool
	// The probe runs until release, not until the caller's context ends:
	// release still needs the session alive to unlock it.
	probeBase := context.WithoutCancel(ctx)
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(workLeaseProbeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			probeCtx, cancel := context.WithTimeout(probeBase, workLeaseProbeTimeout)
			err := conn.PingContext(probeCtx)
			cancel()
			if err != nil {
				dead.Store(true)
				slog.Error("backup: cluster work lease lost, canceling the operation", "err", err)
				lost()
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-stopped // the probe and the unlock must not share the connection
		if dead.Load() {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
			_ = conn.Close()
			return
		}
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
	return OnlineBudget()
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
