package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// TestReadLineHonorsCancellation verifies the restore confirmation prompt
// returns on Ctrl-C (context cancellation) instead of blocking on stdin.
func TestReadLineHonorsCancellation(t *testing.T) {
	got, err := readLine(t.Context(), strings.NewReader("RESTORE\n"))
	if err != nil || strings.TrimSpace(got) != "RESTORE" {
		t.Fatalf("line = %q, %v", got, err)
	}
	if got, err := readLine(t.Context(), strings.NewReader("RESTORE")); err != nil || got != "RESTORE" {
		t.Fatalf("unterminated line = %q, %v", got, err)
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := readLine(ctx, pr); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked prompt err = %v", err)
	}
}

// TestRestoreReconcileContextOutlivesSignalCancel verifies a committed restore
// still gets its bounded reconciliation window after the CLI signal context ends.
func TestRestoreReconcileContextOutlivesSignalCancel(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()

	ctx, cancel := restoreReconcileContext(parent)
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatalf("reconciliation context inherited cancellation: %v", ctx.Err())
	default:
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 2*time.Minute {
		t.Fatalf("reconciliation deadline = %v, ok=%v", deadline, ok)
	}
}
