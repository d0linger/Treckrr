package server

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/store"
)

const (
	loginMaxFails = 5
	loginWindow   = 15 * time.Minute

	// Account-scoped login throttle: a higher threshold and longer window than
	// the per-IP limiter. It bounds a distributed (many-IP) guessing campaign
	// against a single account without making an account-lockout DoS practical —
	// usernames stay non-enumerable via the constant-time miss path, so an
	// attacker can't reliably tell which usernames to target, and the threshold
	// is far above any legitimate user's failure rate.
	accountMaxFails = 30
	accountWindow   = time.Hour

	// Public share-link throttle. /s/beleg/{token} is the one unauthenticated
	// route that reaches the database, and it runs several queries per hit, so an
	// unthrottled scanner is free DB load even though the 256-bit token makes
	// guessing hopeless. Only MISSES are counted, so a visitor holding a working
	// link never approaches the threshold — it is a scanner signal, not a request
	// budget. Set well above the handful of retries a human with a stale or
	// revoked link will produce before giving up.
	shareMaxMisses = 20
	shareWindow    = 15 * time.Minute
)

// loginLimiter is a Postgres-backed sliding-window limiter for login and other
// sensitive actions, keyed by client IP or user. Persisting the state means it
// survives restarts and is shared across instances. On a DB error it fails open
// (does not lock users out) rather than fail closed — but the error is logged so
// the degraded state is observable rather than silent.
type loginLimiter struct{ store *store.Store }

func newLoginLimiter(st *store.Store) *loginLimiter { return &loginLimiter{store: st} }

// blocked reports whether the key currently exceeds the per-IP failure threshold.
func (l *loginLimiter) blocked(ctx context.Context, key string) bool {
	b, err := l.store.RateLimitBlocked(ctx, key, loginMaxFails, loginWindow)
	if err != nil {
		slog.Warn("ratelimit degraded", "key", sanitizeLog(key), "err", sanitizeLog(err.Error()))
		return false // fail open, but visibly
	}
	return b
}

// fail records a failed attempt and returns the count within the active window.
func (l *loginLimiter) fail(ctx context.Context, key string) int {
	n, err := l.store.RateLimitFail(ctx, key, loginWindow)
	if err != nil {
		slog.Warn("ratelimit fail-record failed", "key", sanitizeLog(key), "err", sanitizeLog(err.Error()))
	}
	return n
}

// reset clears the key after a successful attempt.
func (l *loginLimiter) reset(ctx context.Context, key string) {
	if err := l.store.RateLimitReset(ctx, key); err != nil {
		slog.Warn("ratelimit reset failed", "key", sanitizeLog(key), "err", sanitizeLog(err.Error()))
	}
}

// accountKey namespaces the account-scoped login limiter by normalized username,
// so failures are counted per target account independent of source IP.
func accountKey(username string) string {
	return "pwuser:" + strings.ToLower(strings.TrimSpace(username))
}

// accountBlocked reports whether login attempts for the given username exceed the
// account-scoped threshold, independent of which IP(s) they came from.
func (l *loginLimiter) accountBlocked(ctx context.Context, username string) bool {
	b, err := l.store.RateLimitBlocked(ctx, accountKey(username), accountMaxFails, accountWindow)
	if err != nil {
		slog.Warn("ratelimit degraded (account)", "err", sanitizeLog(err.Error()))
		return false // fail open, but visibly
	}
	return b
}

// accountFail records a failed login attempt against the target account.
func (l *loginLimiter) accountFail(ctx context.Context, username string) {
	if _, err := l.store.RateLimitFail(ctx, accountKey(username), accountWindow); err != nil {
		slog.Warn("ratelimit fail-record failed (account)", "err", sanitizeLog(err.Error()))
	}
}

// shareKey namespaces the public share-link miss counter by client IP, keeping it
// clear of the login buckets so a blocked scanner cannot also lock out a login.
func shareKey(ip string) string { return "share:" + ip }

// shareBlocked reports whether this client has produced enough share-token misses
// to look like it is scanning.
func (l *loginLimiter) shareBlocked(ctx context.Context, ip string) bool {
	b, err := l.store.RateLimitBlocked(ctx, shareKey(ip), shareMaxMisses, shareWindow)
	if err != nil {
		slog.Warn("ratelimit degraded (share)", "err", sanitizeLog(err.Error()))
		return false // fail open, but visibly
	}
	return b
}

// shareMiss records one unresolvable share token. There is deliberately no reset
// counterpart: a hit on a valid link must not clear a scanner's tally, and the
// sliding window ages the count out on its own.
func (l *loginLimiter) shareMiss(ctx context.Context, ip string) {
	if _, err := l.store.RateLimitFail(ctx, shareKey(ip), shareWindow); err != nil {
		slog.Warn("ratelimit fail-record failed (share)", "err", sanitizeLog(err.Error()))
	}
}

// accountReset clears the account-scoped counter after a successful login. A
// failure here would leave the account bucket active until it ages out, so log
// it rather than dropping it silently.
func (l *loginLimiter) accountReset(ctx context.Context, username string) {
	if err := l.store.RateLimitReset(ctx, accountKey(username)); err != nil {
		slog.Warn("ratelimit account reset failed", "err", sanitizeLog(err.Error()))
	}
}
