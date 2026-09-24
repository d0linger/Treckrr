package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/models"
)

func TestRestoreAdmissionAndStickyFailure(t *testing.T) {
	for _, success := range []bool{false, true} {
		s := &Server{}
		finish, err := s.beginRestore(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.beginRestore(t.Context()); err == nil {
			t.Fatal("concurrent restore admitted")
		}
		if !s.maintenance.Load() {
			t.Fatal("second restore cleared maintenance")
		}
		finish(success)
		if s.maintenance.Load() == success {
			t.Fatalf("maintenance state after success=%v", success)
		}
		if !s.activity.TryLock() {
			t.Fatal("activity lock leaked")
		}
		s.activity.Unlock()
	}
}

type unreadBackupBody struct{ read bool }

func (b *unreadBackupBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("must not read")
}
func (*unreadBackupBody) Close() error { return nil }

func TestBusyBackupRejectedBeforeReadingBody(t *testing.T) {
	s := &Server{backup: backup.New(backup.Options{}, nil)}
	_, release, err := s.backup.AcquireWork(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	body := &unreadBackupBody{}
	req := httptest.NewRequest(http.MethodPost, "/admin/backup/validate", body)
	req = req.WithContext(context.WithValue(req.Context(), userCacheKey, &userCache{user: &models.User{IsAdmin: true}, done: true}))
	w := httptest.NewRecorder()
	s.limitBody(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("busy handler reached") })).ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable || body.read {
		t.Fatalf("status=%d body read=%v", w.Code, body.read)
	}
}

func TestRestoreWaitsForBackgroundAndCancellationReopensGate(t *testing.T) {
	s := &Server{}
	entered, leave, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() { defer close(done); s.Background(t.Context(), func() { close(entered); <-leave }) }()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if _, err := s.beginRestore(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("active job was not drained: %v", err)
	}
	if s.maintenance.Load() {
		t.Fatal("canceled drain left maintenance enabled before restore")
	}
	close(leave)
	<-done
	finish, err := s.beginRestore(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	called := false
	s.Background(t.Context(), func() { called = true })
	if called {
		t.Fatal("background work entered maintenance")
	}
	finish(true)
}

func TestRestoreCancelsAndDrainsRegisteredBackgroundTask(t *testing.T) {
	s := &Server{}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.BackgroundTask(t.Context(), func(ctx context.Context) {
			close(started)
			<-ctx.Done()
		})
	}()
	<-started
	finish, err := s.beginRestore(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	default:
		t.Fatal("restore acquired lease before background task drained")
	}
	finish(true)
}

func TestFailClosedCancelsBackgroundAndReadiness(t *testing.T) {
	s := &Server{}
	canceled := make(chan struct{})
	go s.BackgroundTask(t.Context(), func(ctx context.Context) {
		<-ctx.Done()
		close(canceled)
	})
	for i := 0; i < 100; i++ {
		s.backgroundMu.Lock()
		registered := len(s.backgroundTasks) == 1
		s.backgroundMu.Unlock()
		if registered {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.FailClosed()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("fail-closed did not cancel background work")
	}
	if !s.maintenance.Load() {
		t.Fatal("fail-closed left readiness enabled")
	}
}

func TestRestoreLeaseFailureIsFailClosed(t *testing.T) {
	s := &Server{}
	s.SetRestoreLease(func(context.Context) (func() error, error) { return nil, errors.New("another instance") })
	if _, err := s.beginRestore(t.Context()); err == nil || s.maintenance.Load() {
		t.Fatalf("acquire failure: %v", err)
	}
	s.SetRestoreLease(func(context.Context) (func() error, error) {
		return func() error { return errors.New("connection lost") }, nil
	})
	finish, err := s.beginRestore(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	finish(true)
	if !s.maintenance.Load() {
		t.Fatal("failed release reopened traffic")
	}
}

func TestRestoreRequestDrainsItsPreflightWithoutSelfDeadlock(t *testing.T) {
	s := &Server{}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/admin/backup/restore", nil)
	s.maintenanceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.activity.TryLock() {
			s.activity.Unlock()
			t.Fatal("restore preflight was outside the drain gate")
		}
		finish, err := s.beginRestore(r.Context())
		if err != nil {
			t.Fatalf("restore could not upgrade its own admission: %v", err)
		}
		finish(true)
	})).ServeHTTP(httptest.NewRecorder(), req)
	if !s.activity.TryLock() {
		t.Fatal("read admission leaked")
	}
	s.activity.Unlock()
}

func TestMaintenanceExemptRoutesNeverResolveSessions(t *testing.T) {
	s := &Server{} // no config/store: any attempt to resolve a cookie would panic
	for _, path := range []string{"/livez", "/static/css/app.css"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "restored-session"})
		s.userCacheMW(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			if s.currentUser(r) != nil {
				t.Fatal("DB-free route resolved a user")
			}
		})).ServeHTTP(httptest.NewRecorder(), req)
	}
}
