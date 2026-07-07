package server

import (
	"context"
	"time"

	"treckrr/internal/store"
)

const (
	loginMaxFails = 5
	loginWindow   = 15 * time.Minute
)

// loginLimiter is a Postgres-backed sliding-window limiter for login and other
// sensitive actions, keyed by client IP or user. Persisting the state means it
// survives restarts and is shared across instances. On a DB error it fails open
// (does not lock users out) rather than fail closed.
type loginLimiter struct{ store *store.Store }

func newLoginLimiter(st *store.Store) *loginLimiter { return &loginLimiter{store: st} }

// blocked reports whether the key currently exceeds the failure threshold.
func (l *loginLimiter) blocked(ctx context.Context, key string) bool {
	b, err := l.store.RateLimitBlocked(ctx, key, loginMaxFails, loginWindow)
	return err == nil && b
}

// fail records a failed attempt and returns the count within the active window.
func (l *loginLimiter) fail(ctx context.Context, key string) int {
	n, _ := l.store.RateLimitFail(ctx, key, loginWindow)
	return n
}

// reset clears the key after a successful attempt.
func (l *loginLimiter) reset(ctx context.Context, key string) {
	_ = l.store.RateLimitReset(ctx, key)
}
