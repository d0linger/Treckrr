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

// TestResetTotpRevokesSessionsIntegration pins the store contract the admin
// "2FA zurücksetzen" handler depends on: turning the second factor off and then
// terminating the target's sessions has to actually leave no usable session.
//
// The handler previously reset TOTP and cleared the recovery codes but left every
// session alive. The usual reason to press that button is a compromised account
// or a lost authenticator — and in the compromised case the old behaviour was
// worse than doing nothing: the attacker keeps the session AND the second factor
// is now off.
func TestResetTotpRevokesSessionsIntegration(t *testing.T) {
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

	f := fixtures{UsernameLike: "tfareset%"}
	purgeFixtures(t, ctx, pool, f)
	defer purgeFixtures(t, ctx, pool, f)

	username := fmt.Sprintf("tfareset%d", os.Getpid())
	uid, err := st.CreateUser(ctx, username, "correct-horse-battery", "editor")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := st.SetTotp(ctx, uid, true, "JBSWY3DPEHPK3PXP"); err != nil {
		t.Fatalf("enable totp: %v", err)
	}

	// Two live sessions, as a compromised account would have.
	for i := range 2 {
		if _, err := st.CreateSession(ctx, uid, time.Hour, "ua", "192.0.2.1"); err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
	}
	sessions, err := st.ListSessionsForUser(ctx, uid)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("expected 2 live sessions, got %d (err %v)", len(sessions), err)
	}

	// What the handler does: disable the factor, clear recovery codes, revoke.
	if err := st.SetTotp(ctx, uid, false, ""); err != nil {
		t.Fatalf("reset totp: %v", err)
	}
	_ = st.ClearRecoveryCodes(ctx, uid)
	if err := st.DeleteUserSessionsExcept(ctx, uid, ""); err != nil {
		t.Fatalf("revoke sessions: %v", err)
	}

	left, err := st.ListSessionsForUser(ctx, uid)
	if err != nil {
		t.Fatalf("list after revoke: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("%d session(s) survived the 2FA reset; a compromised account keeps "+
			"access with the second factor now switched OFF", len(left))
	}
}
