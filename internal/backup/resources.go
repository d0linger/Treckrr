package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// ErrBusy rejects another large backup job before it allocates memory.
var ErrBusy = errors.New("another backup operation is running")

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
	lease := &workLease{service: s}
	lease.active.Store(true)
	var once sync.Once
	release := func() {
		once.Do(func() {
			lease.active.Store(false)
			<-s.workSem
		})
	}
	return context.WithValue(ctx, workKey{}, lease), release, nil
}

func (s *Service) maxBytes() int64 {
	if s.opt.MaxBytes > 16<<30 {
		return 16 << 30 // avoid overflow in max+1 readers, even for internal callers
	}
	if s.opt.MaxBytes > 0 {
		return s.opt.MaxBytes
	}
	return maxS3ObjectBytes
}

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

func (s *Service) readFile(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-supplied offline CLI path
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return s.readArchive(f)
}

type limitedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("backup exceeds configured memory budget")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}
