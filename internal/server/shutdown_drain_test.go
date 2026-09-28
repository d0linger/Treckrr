package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestBeginShutdownCancelsAndDrainsBackgroundTasks verifies the process drain
// stops a running task between units of work, waits for it, and refuses new
// background work afterwards.
func TestBeginShutdownCancelsAndDrainsBackgroundTasks(t *testing.T) {
	s := &Server{}
	started, finished := make(chan struct{}), make(chan struct{})
	go func() {
		// Detached like the batch Mahnlauf: only the registry can cancel it.
		s.BackgroundTask(context.WithoutCancel(t.Context()), func(ctx context.Context) {
			close(started)
			<-ctx.Done()
			time.Sleep(20 * time.Millisecond) // the mail in flight settles
			close(finished)
		})
	}()
	<-started
	s.BeginShutdown()
	if err := s.WaitBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("WaitBackground returned before the task finished")
	}
	ran := false
	s.BackgroundTask(t.Context(), func(context.Context) { ran = true })
	if ran {
		t.Fatal("background task admitted after shutdown began")
	}
	if s.maintenanceActive() {
		t.Fatal("shutdown drain must not flip restore maintenance")
	}
}

// TestWaitBackgroundHonorsDeadline verifies the drain is bounded.
func TestWaitBackgroundHonorsDeadline(t *testing.T) {
	s := &Server{}
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.BackgroundTask(context.WithoutCancel(t.Context()), func(context.Context) {
			close(started)
			<-release // ignores cancellation
		})
	}()
	<-started
	s.BeginShutdown()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := s.WaitBackground(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(release)
	<-done
}

// TestGoBackgroundRunsDetachedAndIsDrained verifies async work (the CC copy)
// outlives its request context but is still drained at shutdown.
func TestGoBackgroundRunsDetachedAndIsDrained(t *testing.T) {
	s := &Server{}
	reqCtx, cancelReq := context.WithCancel(t.Context())
	started := make(chan struct{})
	finished := make(chan error, 1)
	s.goBackground(reqCtx, time.Minute, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		finished <- ctx.Err()
	})
	<-started
	cancelReq() // the request ended; the copy must keep running
	select {
	case err := <-finished:
		t.Fatalf("async work canceled with its request: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	s.BeginShutdown()
	if err := s.WaitBackground(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("async work not canceled by shutdown: %v", err)
	}
	var ran atomic.Bool
	s.goBackground(t.Context(), time.Minute, func(context.Context) { ran.Store(true) })
	time.Sleep(10 * time.Millisecond)
	if ran.Load() {
		t.Fatal("async work started after shutdown began")
	}
}
