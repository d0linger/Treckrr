package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"treckrr/internal/store"
)

func TestAuthenticateUserSafetyBoundaries(t *testing.T) {
	// Since we are checking that AuthenticateUser returns ErrNotFound immediately
	// on overly long username or password without hitting the database, we can
	// construct the Store with a nil sql.DB! Any request that passes validation
	// will attempt to access the DB and panic on a nil pointer dereference.
	st := store.New(nil, "test-encryption-secret-at-least-32-bytes!!")
	ctx := context.Background()

	t.Run("username over 100 runes rejected immediately", func(t *testing.T) {
		longUsername := strings.Repeat("a", 101)
		u, err := st.AuthenticateUser(ctx, longUsername, "password")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("expected ErrNotFound for over-limit username, got %v", err)
		}
		if u != nil {
			t.Errorf("expected nil user for over-limit username, got %v", u)
		}
	})

	t.Run("username exactly 100 runes passes input check but hits db", func(t *testing.T) {
		var panicked bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
				}
			}()
			username := strings.Repeat("a", 100)
			_, _ = st.AuthenticateUser(ctx, username, "password")
		}()
		if !panicked {
			t.Error("expected panic (nil db dereference) as proof that the 100-rune username bypassed validation, but it did not panic")
		}
	})

	t.Run("password over 72 bytes rejected immediately", func(t *testing.T) {
		longPassword := strings.Repeat("p", 73)
		u, err := st.AuthenticateUser(ctx, "normaluser", longPassword)
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("expected ErrNotFound for over-limit password, got %v", err)
		}
		if u != nil {
			t.Errorf("expected nil user for over-limit password, got %v", u)
		}
	})

	t.Run("password exactly 72 bytes passes input check but hits db", func(t *testing.T) {
		var panicked bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
				}
			}()
			password := strings.Repeat("p", 72)
			_, _ = st.AuthenticateUser(ctx, "normaluser", password)
		}()
		if !panicked {
			t.Error("expected panic (nil db dereference) as proof that the 72-byte password bypassed validation, but it did not panic")
		}
	})
}
