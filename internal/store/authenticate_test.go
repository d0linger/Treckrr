package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"treckrr/internal/store"
)

// AuthenticateUser must reject oversized credentials before touching the DB or
// bcrypt. We construct the Store with a nil *sql.DB: anything that passes the
// input guard reaches the DB and panics on the nil pointer — so a panic proves
// the value passed the guard, and ErrNotFound proves it was rejected up front.
func TestAuthenticateUserSafetyBoundaries(t *testing.T) {
	st := store.New(nil, "test-encryption-secret-at-least-32-bytes!!")
	ctx := context.Background()

	t.Run("username over 100 runes rejected immediately", func(t *testing.T) {
		u, err := st.AuthenticateUser(ctx, strings.Repeat("a", 101), "password")
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("expected ErrNotFound for over-limit username, got %v", err)
		}
		if u != nil {
			t.Errorf("expected nil user for over-limit username, got %v", u)
		}
	})

	// 100 multibyte runes = 300 bytes: this passes the 100-rune limit yet would
	// fail a naive byte-length check, proving the guard counts runes, not bytes.
	t.Run("username exactly 100 runes passes input check but hits db", func(t *testing.T) {
		var panicked bool
		func() {
			defer func() {
				if r := recover(); r != nil {
					panicked = true
				}
			}()
			_, _ = st.AuthenticateUser(ctx, strings.Repeat("世", 100), "password")
		}()
		if !panicked {
			t.Error("expected panic (nil db) proving the 100-rune username bypassed validation, but it did not panic")
		}
	})

	t.Run("password over 72 bytes rejected immediately", func(t *testing.T) {
		u, err := st.AuthenticateUser(ctx, "normaluser", strings.Repeat("p", 73))
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
			_, _ = st.AuthenticateUser(ctx, "normaluser", strings.Repeat("p", 72))
		}()
		if !panicked {
			t.Error("expected panic (nil db) proving the 72-byte password bypassed validation, but it did not panic")
		}
	})
}
