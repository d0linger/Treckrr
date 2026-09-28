//go:build integration

package store_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestEnsureAdminNeverPromotesWithoutResetIntegration pins AUTH-02: a normal
// boot neither promotes a non-admin named ADMIN_USERNAME nor re-creates a
// missing bootstrap admin while another admin exists; the explicit reset still
// works as break-glass.
func TestEnsureAdminNeverPromotesWithoutResetIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	role := func(name string) string {
		var r string
		if err := pool.QueryRowContext(ctx, `SELECT role FROM users WHERE username=$1`, name).Scan(&r); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ""
			}
			t.Fatal(err)
		}
		return r
	}

	// No admin at all: the bootstrap admin is created.
	if err := st.EnsureAdmin(ctx, "boss", "Bootstrap-pass-123", false); err != nil {
		t.Fatal(err)
	}
	if role("boss") != models.RoleAdmin {
		t.Fatal("bootstrap admin not created on an empty instance")
	}

	// A demoted / re-assigned account under ADMIN_USERNAME stays what it is.
	if _, err := st.CreateUser(ctx, "admin", "Viewer-pass-123", models.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureAdmin(ctx, "admin", "Bootstrap-pass-123", false); err != nil {
		t.Fatal(err)
	}
	if got := role("admin"); got != models.RoleViewer {
		t.Fatalf("normal boot promoted the viewer to %q", got)
	}

	// A renamed-away bootstrap name is not revived while an admin exists.
	if err := st.EnsureAdmin(ctx, "ghost", "Bootstrap-pass-123", false); err != nil {
		t.Fatal(err)
	}
	if role("ghost") != "" {
		t.Fatal("bootstrap admin re-created although an admin exists")
	}

	// Break-glass: explicit reset promotes and resets deliberately.
	if err := st.EnsureAdmin(ctx, "admin", "Break-glass-pass-456", true); err != nil {
		t.Fatal(err)
	}
	if role("admin") != models.RoleAdmin {
		t.Fatal("explicit reset did not promote")
	}
	u, err := st.AuthenticateUser(ctx, "admin", "Break-glass-pass-456")
	if err != nil || !u.MustChangePassword {
		t.Fatalf("reset password not applied with forced change: %v", err)
	}
}

func addTestPasskey(t *testing.T, st *store.Store, userID int64, id string, count uint32) {
	t.Helper()
	if err := st.AddWebauthnCredential(t.Context(), userID, models.WebauthnCredential{
		CredentialID: []byte(id), PublicKey: []byte("pk"), AAGUID: make([]byte, 16), SignCount: count, Name: id,
	}); err != nil {
		t.Fatal(err)
	}
}

func passkeyCount(t *testing.T, pool *sql.DB, userID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRowContext(t.Context(), `SELECT count(*) FROM webauthn_credentials WHERE user_id=$1`, userID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAdminResetsRevokePasskeysIntegration pins AUTH-03: both admin resets
// evict passkeys (and say so in the audit), a self-service change keeps them.
func TestAdminResetsRevokePasskeysIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	uid, err := st.CreateUser(ctx, "passkey-owner", "Owner-pass-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}

	addTestPasskey(t, st, uid, "self-change", 0)
	session, err := st.CreateSession(ctx, uid, time.Hour, "test", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChangePassword(ctx, store.PasswordChange{
		UserID: uid, CurrentPassword: "Owner-pass-123", NewPassword: "Owner-pass-456", CurrentToken: session,
		TTL: time.Hour, AbsoluteTTL: 24 * time.Hour, UserAgent: "test", IP: "192.0.2.1",
	}); err != nil {
		t.Fatal(err)
	}
	if passkeyCount(t, pool, uid) != 1 {
		t.Fatal("self-service password change removed the user's own passkey")
	}

	if err := st.ResetPassword(ctx, uid, "Admin-reset-789", true); err != nil {
		t.Fatal(err)
	}
	if passkeyCount(t, pool, uid) != 0 {
		t.Fatal("admin password reset kept a passkey")
	}

	addTestPasskey(t, st, uid, "after-reset", 0)
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: uid, Enabled: true, Secret: totpTestFixture, RecoveryHashes: []string{"h"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.ResetTotpForUser(ctx, uid); err != nil {
		t.Fatal(err)
	}
	if passkeyCount(t, pool, uid) != 0 {
		t.Fatal("admin 2FA reset kept a passkey")
	}
	var noted int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM audit_log
		WHERE action IN ('password_reset','2fa_reset') AND detail LIKE '%1 Passkey(s) widerrufen%'`).Scan(&noted); err != nil {
		t.Fatal(err)
	}
	if noted != 2 {
		t.Fatalf("audited passkey revocations = %d, want 2", noted)
	}
}

// TestEnrollmentStepConsumedIntegration pins AUTH-08: enabling TOTP records the
// confirmation code's step, so that code is already spent.
func TestEnrollmentStepConsumedIntegration(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	uid, err := st.CreateUser(ctx, "enroll-step", "Enroll-pass-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	const step = 57_000_000
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{
		UserID: uid, Enabled: true, Secret: totpTestFixture, RecoveryHashes: []string{"h"}, AcceptedStep: step,
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.AcceptTotpStep(ctx, uid, step); err != nil || ok {
		t.Fatalf("enrollment step accepted again: ok=%v err=%v", ok, err)
	}
	if ok, err := st.AcceptTotpStep(ctx, uid, step+1); err != nil || !ok {
		t.Fatalf("next step rejected: ok=%v err=%v", ok, err)
	}
}

// TestTouchWebauthnCredentialMonotonicIntegration pins AUTH-09: the stored
// counter never moves backwards and reports when it did not advance.
func TestTouchWebauthnCredentialMonotonicIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	uid, err := st.CreateUser(ctx, "counter-user", "Counter-pass-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	addTestPasskey(t, st, uid, "counted", 5)
	addTestPasskey(t, st, uid, "synced", 0)
	for _, tc := range []struct {
		id    string
		count uint32
		want  bool
	}{
		{"counted", 7, true},
		{"counted", 6, false}, // lower count racing in late
		{"counted", 7, false}, // not advanced
		{"counted", 0, false}, // reset to zero
		{"synced", 0, true},   // counter-less passkey
		{"missing", 9, false},
	} {
		got, err := st.TouchWebauthnCredential(ctx, []byte(tc.id), tc.count, false)
		if err != nil || got != tc.want {
			t.Fatalf("touch %s=%d: advanced=%v err=%v, want %v", tc.id, tc.count, got, err, tc.want)
		}
	}
	var stored int64
	if err := pool.QueryRowContext(ctx, `SELECT sign_count FROM webauthn_credentials WHERE credential_id=$1`, []byte("counted")).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 7 {
		t.Fatalf("stored counter = %d, want 7", stored)
	}
}

// TestRateLimitRefundKeepsFailuresIntegration pins the AUTH-04 primitive: a
// refund returns one attempt and never erases earlier failures.
func TestRateLimitRefundKeepsFailuresIntegration(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	const key = "192.0.2.10"
	for range 4 {
		if ok, err := st.RateLimitAdmit(ctx, key, 5, time.Minute); err != nil || !ok {
			t.Fatalf("admit: %v %v", ok, err)
		}
	}
	if ok, err := st.RateLimitAdmit(ctx, key, 5, time.Minute); err != nil || !ok { // the success
		t.Fatalf("admit: %v %v", ok, err)
	}
	if err := st.RateLimitRefund(ctx, key); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.RateLimitAdmit(ctx, key, 5, time.Minute); err != nil || !ok {
		t.Fatalf("refunded slot not available: %v %v", ok, err)
	}
	if ok, err := st.RateLimitAdmit(ctx, key, 5, time.Minute); err != nil || ok {
		t.Fatalf("refund erased earlier failures: ok=%v err=%v", ok, err)
	}
	if err := st.RateLimitRefund(ctx, "never-seen"); err != nil {
		t.Fatal(err)
	}
}
