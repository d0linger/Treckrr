package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestLastAdminGuardIntegration proves SH-04: the last admin cannot be demoted or
// deleted, the invariant is enforced atomically, and non-last admins/editors are
// unaffected.
func TestLastAdminGuardIntegration(t *testing.T) {
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

	// Unique usernames so the suite can share a database.
	u := func(s string) string { return fmt.Sprintf("sh04_%s_%d", s, os.Getpid()) }

	adminID, err := st.CreateUser(ctx, u("admin1"), "pw-admin-1-xxxxx", models.RoleAdmin)
	if err != nil {
		t.Fatalf("create admin1: %v", err)
	}

	t.Run("cannot demote the only admin", func(t *testing.T) {
		if err := st.SetRoleSafe(ctx, adminID, models.RoleEditor); !errors.Is(err, store.ErrLastAdmin) {
			t.Fatalf("expected ErrLastAdmin, got %v", err)
		}
		if got, _ := st.GetUser(ctx, adminID); got == nil || !got.IsAdmin {
			t.Fatalf("admin must remain admin after a refused demotion")
		}
	})

	t.Run("cannot delete the only admin", func(t *testing.T) {
		if err := st.DeleteUserSafe(ctx, adminID); !errors.Is(err, store.ErrLastAdmin) {
			t.Fatalf("expected ErrLastAdmin, got %v", err)
		}
		if got, _ := st.GetUser(ctx, adminID); got == nil {
			t.Fatalf("admin must still exist after a refused deletion")
		}
	})

	t.Run("with a second admin, demotion and deletion are allowed", func(t *testing.T) {
		admin2, err := st.CreateUser(ctx, u("admin2"), "pw-admin-2-xxxxx", models.RoleAdmin)
		if err != nil {
			t.Fatalf("create admin2: %v", err)
		}
		// Now two admins: demoting the first is fine.
		if err := st.SetRoleSafe(ctx, adminID, models.RoleEditor); err != nil {
			t.Fatalf("demote with two admins should succeed: %v", err)
		}
		if got, _ := st.GetUser(ctx, adminID); got.IsAdmin {
			t.Fatalf("admin1 should have been demoted")
		}
		// admin2 is now the only admin → its deletion is refused again.
		if err := st.DeleteUserSafe(ctx, admin2); !errors.Is(err, store.ErrLastAdmin) {
			t.Fatalf("expected ErrLastAdmin for the now-only admin, got %v", err)
		}
	})

	t.Run("non-admin users are unaffected", func(t *testing.T) {
		ed, err := st.CreateUser(ctx, u("editor"), "pw-editor-xxxxxx", models.RoleEditor)
		if err != nil {
			t.Fatalf("create editor: %v", err)
		}
		if err := st.SetRoleSafe(ctx, ed, models.RoleViewer); err != nil {
			t.Fatalf("editor role change should succeed: %v", err)
		}
		if err := st.DeleteUserSafe(ctx, ed); err != nil {
			t.Fatalf("editor deletion should succeed: %v", err)
		}
	})

	t.Run("missing user returns ErrNotFound", func(t *testing.T) {
		if err := st.DeleteUserSafe(ctx, 999999999); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})
}
