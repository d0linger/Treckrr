package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"
)

type restoreAdmissionKey struct{}

type backgroundTask struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// SetRestoreLease supplies the serving process's database-scoped restore lease.
// Configure it before starting the HTTP server or any background workers.
func (s *Server) SetRestoreLease(acquire func(context.Context) (func() error, error)) {
	s.restoreLease = acquire
}

// Background admits maintenance jobs through the same drain gate as HTTP.
func (s *Server) Background(ctx context.Context, work func()) {
	if ctx.Err() != nil || s.maintenanceActive() {
		return
	}
	s.activity.RLock()
	defer s.activity.RUnlock()
	if ctx.Err() == nil && !s.maintenanceActive() {
		work()
	}
}

// BackgroundTask registers cancellable background work without holding the
// request activity lock across external I/O. Restore admission first prevents
// new registrations, then cancels and drains every registered task before it
// takes the exclusive database lease.
func (s *Server) BackgroundTask(ctx context.Context, work func(context.Context)) {
	if ctx.Err() != nil || s.maintenanceActive() {
		return
	}
	taskCtx, cancel := context.WithCancel(ctx)
	task := backgroundTask{cancel: cancel, done: make(chan struct{})}

	s.backgroundMu.Lock()
	if s.maintenanceActive() {
		s.backgroundMu.Unlock()
		cancel()
		return
	}
	if s.backgroundTasks == nil {
		s.backgroundTasks = make(map[uint64]backgroundTask)
	}
	s.backgroundNext++
	id := s.backgroundNext
	s.backgroundTasks[id] = task
	s.backgroundMu.Unlock()

	defer func() {
		cancel()
		s.backgroundMu.Lock()
		delete(s.backgroundTasks, id)
		close(task.done)
		s.backgroundMu.Unlock()
	}()
	work(taskCtx)
}

// cancelBackground asks every registered task to stop and waits for the task
// registry to drain within ctx.
func (s *Server) cancelBackground(ctx context.Context) error {
	s.backgroundMu.Lock()
	tasks := make([]backgroundTask, 0, len(s.backgroundTasks))
	for _, task := range s.backgroundTasks {
		tasks = append(tasks, task)
		task.cancel()
	}
	s.backgroundMu.Unlock()
	for _, task := range tasks {
		select {
		case <-task.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// FailClosed immediately removes this process from readiness and asks every
// registered background task to stop. It is used when the PostgreSQL session
// holding the restore-exclusion lease is lost.
func (s *Server) FailClosed() {
	s.leaseLost.Store(true)
	s.setMaintenance(true)
	s.backgroundMu.Lock()
	for _, task := range s.backgroundTasks {
		task.cancel()
	}
	s.backgroundMu.Unlock()
}

// beginRestore drains admitted work, acquires exclusive restore ownership, and
// returns a completion callback that reopens traffic only after reconciliation.
func (s *Server) beginRestore(ctx context.Context) (func(bool), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.leaseLost.Load() {
		return nil, errors.New("application maintenance lease was lost")
	}
	if !s.maintenance.CompareAndSwap(false, true) {
		return nil, errors.New("a restore is already in progress")
	}
	if release, ok := ctx.Value(restoreAdmissionKey{}).(func()); ok {
		release()
	}
	if err := s.cancelBackground(ctx); err != nil {
		s.clearRestoreMaintenance()
		return nil, err
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !s.activity.TryLock() {
		select {
		case <-ctx.Done():
			s.clearRestoreMaintenance()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
	if err := ctx.Err(); err != nil {
		s.activity.Unlock()
		s.clearRestoreMaintenance()
		return nil, err
	}
	if s.leaseLost.Load() {
		s.activity.Unlock()
		return nil, errors.New("application maintenance lease was lost")
	}
	release := func() error { return nil }
	if s.restoreLease != nil {
		var err error
		release, err = s.restoreLease(ctx)
		if err != nil {
			s.activity.Unlock()
			s.clearRestoreMaintenance()
			return nil, err
		}
	}
	return func(reconciled bool) {
		if err := release(); err != nil {
			slog.Error("restore: lease release failed; maintenance remains enabled", "err", err)
			reconciled = false
		}
		if reconciled {
			s.clearRestoreMaintenance()
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
