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
