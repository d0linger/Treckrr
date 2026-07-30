package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"treckrr/internal/store"
)

func TestAuthenticateUser_InputValidation(t *testing.T) {
	// Create a Store with a nil db.
	// This is safe because validation checks are performed before any DB queries or bcrypt operations.
	st := store.New(nil, "test-encryption-key-at-least-32-bytes!!")

	ctx := context.Background()

	t.Run("username too long rejected immediately", func(t *testing.T) {
		longUsername := strings.Repeat("a", 101)
		user, err := st.AuthenticateUser(ctx, longUsername, "password")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("expected ErrNotFound for long username, got error: %v, user: %v", err, user)
		}
	})

	t.Run("password too long rejected immediately", func(t *testing.T) {
		longPassword := strings.Repeat("p", 73)
		user, err := st.AuthenticateUser(ctx, "validuser", longPassword)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("expected ErrNotFound for long password, got error: %v, user: %v", err, user)
		}
	})

	t.Run("normal input bypasses length validation and attempts query", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("expected a panic (nil pointer dereference on database call) for normal input because db is nil, but it did not panic")
			}
		}()
		_, _ = st.AuthenticateUser(ctx, "validuser", "validpassword")
	})
}
