package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"treckrr/internal/db"
	"treckrr/internal/store"
)

// TestWebauthnCeremonyIntegration proves SH-03: a ceremony is single-use
// (consuming it once succeeds, twice fails) and server-expiring.
func TestWebauthnCeremonyIntegration(t *testing.T) {
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

	t.Run("single use", func(t *testing.T) {
		if err := st.CreateWebauthnCeremony(ctx, "cer-single", []byte(`{"x":1}`), time.Now().Add(time.Minute)); err != nil {
			t.Fatalf("create: %v", err)
		}
		data, err := st.ConsumeWebauthnCeremony(ctx, "cer-single")
		if err != nil || string(data) != `{"x":1}` {
			t.Fatalf("first consume: data=%q err=%v", data, err)
		}
		if _, err := st.ConsumeWebauthnCeremony(ctx, "cer-single"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("replay must fail with ErrNotFound, got %v", err)
		}
	})

	t.Run("expired is rejected and purged", func(t *testing.T) {
		if err := st.CreateWebauthnCeremony(ctx, "cer-expired", []byte(`{}`), time.Now().Add(-time.Minute)); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := st.ConsumeWebauthnCeremony(ctx, "cer-expired"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expired consume must fail with ErrNotFound, got %v", err)
		}
		if err := st.PurgeExpiredWebauthnCeremonies(ctx); err != nil {
			t.Fatalf("purge: %v", err)
		}
		// After purge the row is gone; a consume still reports ErrNotFound.
		if _, err := st.ConsumeWebauthnCeremony(ctx, "cer-expired"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("after purge: %v", err)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		if _, err := st.ConsumeWebauthnCeremony(ctx, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("unknown id must be ErrNotFound, got %v", err)
		}
	})
}
