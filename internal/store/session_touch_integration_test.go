package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestSessionSlideIsThrottledIntegration pins the write-throttling on the session
// row. Refreshing last_seen/expires_at on EVERY authenticated request produced one
// UPDATE per page view against the same hot row. The throttle must cut those
// writes without weakening the sliding expiry or, worse, breaking authentication.
func TestSessionSlideIsThrottledIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	f := fixtures{UsernameLike: "slide%"}
	purgeFixtures(t, ctx, pool, f)
	defer purgeFixtures(t, ctx, pool, f)

	uid, err := st.CreateUser(ctx, fmt.Sprintf("slide%d", os.Getpid()), "correct-horse-battery", "editor")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := st.CreateSession(ctx, uid, 30*24*time.Hour, "ua", "192.0.2.1")
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	th := store.HashToken(token)

	lastSeen := func() time.Time {
		t.Helper()
		var ts time.Time
		if err := pool.QueryRowContext(ctx,
			`SELECT last_seen FROM sessions WHERE token=$1`, th).Scan(&ts); err != nil {
			t.Fatalf("read last_seen: %v", err)
		}
		return ts
	}

	before := lastSeen()

	// Several resolves in quick succession: all must authenticate, none may write.
	for i := range 5 {
		if u, err := st.UserFromSession(ctx, token, 30*24*time.Hour, 90*24*time.Hour); err != nil || u == nil {
			t.Fatalf("resolve #%d failed: %v", i+1, err)
		}
	}
	if got := lastSeen(); !got.Equal(before) {
		t.Fatalf("last_seen moved from %s to %s during 5 rapid resolves — the write "+
			"throttle is not in effect", before, got)
	}

	// Backdate past the interval: the next resolve must refresh, or an actively
	// used session would eventually expire under the user.
	if _, err := pool.ExecContext(ctx,
		`UPDATE sessions SET last_seen = now() - interval '10 minutes' WHERE token=$1`, th); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	stale := lastSeen()
	if u, err := st.UserFromSession(ctx, token, 30*24*time.Hour, 90*24*time.Hour); err != nil || u == nil {
		t.Fatalf("resolve after backdating: %v", err)
	}
	if got := lastSeen(); !got.After(stale) {
		t.Fatalf("last_seen stayed at %s after the interval elapsed — the session "+
			"would stop sliding and expire while in use", stale)
	}
}
