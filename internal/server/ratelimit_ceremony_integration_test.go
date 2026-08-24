package server

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestCeremonyLimiterIntegration pins the regression in the public passkey
// login-begin route: it consulted only the per-IP login limiter, whose counter
// rises exclusively on FAILED password logins. A client that never attempted a
// login therefore never tripped it and could create server-side WebAuthn
// ceremony rows without bound.
//
// It also pins the property that makes charging *successful* begins safe: the
// ceremony counter lives in its own key space, so exhausting it must not touch
// the victim's password-login budget.
func TestCeremonyLimiterIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Registered before the reset cleanup below so LIFO order closes the pool LAST
	// — a plain `defer pool.Close()` would run before t.Cleanup and the resets
	// would fail against a closed database.
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	lim := newLoginLimiter(store.New(pool, "test-encryption-secret"))

	// Unique per run so repeated runs don't inherit a tripped counter.
	ip := fmt.Sprintf("203.0.113.%d", os.Getpid()%200+1)
	t.Cleanup(func() {
		lim.ceremonyReset(ctx, ip)
		lim.reset(ctx, ip)
	})
	lim.ceremonyReset(ctx, ip)
	lim.reset(ctx, ip)

	if lim.ceremonyBlocked(ctx, ip) {
		t.Fatal("a fresh IP is already ceremony-blocked")
	}

	// Charge right up to the threshold — a real user needs one begin per login, so
	// this must not trip early.
	for i := range ceremonyMaxBegins - 1 {
		lim.ceremonyBegin(ctx, ip)
		if lim.ceremonyBlocked(ctx, ip) {
			t.Fatalf("blocked after %d begins, want room up to %d", i+1, ceremonyMaxBegins)
		}
	}

	// The one that reaches the threshold closes it.
	lim.ceremonyBegin(ctx, ip)
	if !lim.ceremonyBlocked(ctx, ip) {
		t.Fatalf("still not blocked after %d begins; the flood is unbounded", ceremonyMaxBegins)
	}

	// The password-login budget for the same IP must be untouched: charging a
	// successful begin to the shared login counter would let a few legitimate
	// passkey clicks lock the user out of password login entirely.
	if lim.blocked(ctx, ip) {
		t.Fatal("ceremony flood also blocked password login — the key spaces are not separate")
	}

	// A completed passkey login clears the ceremony budget.
	lim.ceremonyReset(ctx, ip)
	if lim.ceremonyBlocked(ctx, ip) {
		t.Fatal("ceremonyReset did not clear the counter")
	}
}
