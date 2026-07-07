package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"treckrr/internal/db"
	"treckrr/internal/store"
)

// TestRateLimitIntegration exercises the Postgres-backed limiter against a real
// database. It runs only when TEST_DATABASE_URL is set (see the integration
// docker run); otherwise it is skipped so unit runs stay hermetic.
func TestRateLimitIntegration(t *testing.T) {
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
	st := store.New(pool)

	const key = "test:1.2.3.4"
	const max = 5
	const window = time.Minute
	_ = st.RateLimitReset(ctx, key)

	blocked := func() bool {
		b, err := st.RateLimitBlocked(ctx, key, max, window)
		if err != nil {
			t.Fatalf("blocked: %v", err)
		}
		return b
	}

	if blocked() {
		t.Fatal("fresh key must not be blocked")
	}
	for i := 1; i <= max; i++ {
		n, err := st.RateLimitFail(ctx, key, window)
		if err != nil {
			t.Fatalf("fail #%d: %v", i, err)
		}
		if n != i {
			t.Fatalf("fail count = %d, want %d", n, i)
		}
	}
	if !blocked() {
		t.Fatalf("key must be blocked after %d fails", max)
	}
	if err := st.RateLimitReset(ctx, key); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if blocked() {
		t.Fatal("key must not be blocked after reset")
	}

	// A fail with a zero window immediately falls outside the window, so the
	// counter restarts at 1 rather than accumulating.
	if _, err := st.RateLimitFail(ctx, key, 0); err != nil {
		t.Fatalf("fail zero-window: %v", err)
	}
	if b, _ := st.RateLimitBlocked(ctx, key, max, 0); b {
		t.Fatal("zero window must never report blocked")
	}
	_ = st.RateLimitReset(ctx, key)
}
