package server

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

func ceremonyTestLimiter(t *testing.T) (*loginLimiter, context.Context) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Registered before any reset cleanup the caller adds, so LIFO order closes the
	// pool last — a plain defer would run first and the resets would hit a closed DB.
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return newLoginLimiter(store.New(pool, "test-encryption-secret")), ctx
}

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
	lim, ctx := ceremonyTestLimiter(t)

	ip := fmt.Sprintf("203.0.113.%d", os.Getpid()%200+1)
	reset := func() { lim.ceremonyReset(ctx, ip); lim.reset(ctx, ip) }
	t.Cleanup(reset)
	reset()

	// Every permit up to the threshold is granted — a real login needs one, so
	// this must not trip early.
	for i := range ceremonyMaxBegins {
		ok, err := lim.allowCeremonyBegin(ctx, ip)
		if err != nil {
			t.Fatalf("allow #%d: %v", i+1, err)
		}
		if !ok {
			t.Fatalf("refused at request %d, want room for %d", i+1, ceremonyMaxBegins)
		}
	}

	// The next one is refused; the flood is bounded.
	if ok, err := lim.allowCeremonyBegin(ctx, ip); err != nil {
		t.Fatalf("allow past threshold: %v", err)
	} else if ok {
		t.Fatalf("still admitted after %d begins; creation is unbounded", ceremonyMaxBegins)
	}

	// The password-login budget for the same IP is untouched: charging successful
	// begins to the shared login counter would let a few legitimate passkey clicks
	// lock the user out of password login entirely.
	if lim.blocked(ctx, ip) {
		t.Fatal("ceremony flood also blocked password login — the key spaces are not separate")
	}

	// A completed passkey login clears the ceremony budget.
	lim.ceremonyReset(ctx, ip)
	if ok, err := lim.allowCeremonyBegin(ctx, ip); err != nil || !ok {
		t.Fatalf("after reset: allowed=%v err=%v, want allowed", ok, err)
	}
}

// TestCeremonyLimiterIsAtomicUnderConcurrency is the reason allowCeremonyBegin
// charges before it decides. A check-then-charge sequence lets a simultaneous
// burst all pass the read before any of them increments, admitting far more than
// the threshold — exactly the flood the limiter exists to stop. The charge is one
// atomic upsert returning the in-window count, so concurrent callers get distinct
// numbers and only the first ceremonyMaxBegins are admitted.
func TestCeremonyLimiterIsAtomicUnderConcurrency(t *testing.T) {
	lim, ctx := ceremonyTestLimiter(t)

	ip := fmt.Sprintf("198.51.100.%d", os.Getpid()%200+1)
	t.Cleanup(func() { lim.ceremonyReset(ctx, ip) })
	lim.ceremonyReset(ctx, ip)

	const burst = 60 // twice the threshold, all at once
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range burst {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := lim.allowCeremonyBegin(ctx, ip)
			if err != nil {
				return // a pool-contention error is a refusal, not an admission
			}
			if ok {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if admitted > ceremonyMaxBegins {
		t.Fatalf("%d of %d concurrent begins admitted, threshold is %d — "+
			"the limiter is not atomic", admitted, burst, ceremonyMaxBegins)
	}
	if admitted == 0 {
		t.Fatalf("no request was admitted out of %d; the limiter is refusing everything", burst)
	}
	t.Logf("admitted %d of %d concurrent begins (threshold %d)", admitted, burst, ceremonyMaxBegins)
}
