package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

type restoreAdmissionKey struct{}

// SetRestoreLease supplies the serving process's database-scoped restore lease.
// Configure it before starting the HTTP server or any background workers.
func (s *Server) SetRestoreLease(acquire func(context.Context) (func() error, error)) {
	s.restoreLease = acquire
}

// Background admits maintenance jobs through the same drain gate as HTTP.
func (s *Server) Background(ctx context.Context, work func()) {
	if ctx.Err() != nil || s.maintenance.Load() {
		return
	}
	s.activity.RLock()
	defer s.activity.RUnlock()
	if ctx.Err() == nil && !s.maintenance.Load() {
		work()
	}
}

func (s *Server) beginRestore(ctx context.Context) (func(bool), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !s.maintenance.CompareAndSwap(false, true) {
		return nil, errors.New("a restore is already in progress")
	}
	if release, ok := ctx.Value(restoreAdmissionKey{}).(func()); ok {
		release()
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !s.activity.TryLock() {
		select {
		case <-ctx.Done():
			s.setMaintenance(false)
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	if err := ctx.Err(); err != nil {
		s.activity.Unlock()
		s.setMaintenance(false)
		return nil, err
	}
	release := func() error { return nil }
	if s.restoreLease != nil {
		var err error
		release, err = s.restoreLease(ctx)
		if err != nil {
			s.activity.Unlock()
			s.setMaintenance(false)
			return nil, err
		}
	}
	return func(reconciled bool) {
		if err := release(); err != nil {
			slog.Error("restore: lease release failed; maintenance remains enabled", "err", err)
			reconciled = false
		}
		if reconciled {
			s.setMaintenance(false)
		}
		s.activity.Unlock()
	}, nil
}

// Keep body parsing/decryption and the response inside one shared backup lease.
// Settings and the small bucket connectivity probe do not allocate archives.
func isMemoryBackupPath(path string) bool {
	switch path {
	case "/admin/backup/run", "/admin/backup/run-scheduled", "/admin/backup/validate",
		"/admin/backup/restore", "/admin/backup/s3/run":
		return true
	}
	return strings.HasPrefix(path, "/admin/backup/file/") || strings.HasPrefix(path, "/admin/backup/s3/file/")
}
