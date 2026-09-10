package store_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

const totpTestFixture = "JBSWY3DPEHPK3PXP" // #nosec G101 -- public test seed, not a credential

func TestCredentialRotationAtomicIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	userID, err := st.CreateUser(ctx, "rotate-user", "original-pass-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithAuditActor(ctx, store.AuditActor{UserID: &userID, Username: "rotate-user"})
	oldToken, err := st.CreateSession(ctx, userID, time.Hour, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	otherToken, err := st.CreateSession(ctx, userID, time.Hour, "other", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	change := store.PasswordChange{UserID: userID, CurrentPassword: "original-pass-123", NewPassword: "replacement-pass-456", CurrentToken: oldToken, TTL: time.Hour, AbsoluteTTL: 24 * time.Hour}
	if _, err := pool.ExecContext(ctx, `CREATE FUNCTION reject_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit unavailable'; END $$;
		CREATE TRIGGER reject_test_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_test_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChangePassword(ctx, change); err == nil {
		t.Fatal("audit failure must roll back password rotation")
	}
	if _, err := st.AuthenticateUser(ctx, "rotate-user", change.CurrentPassword); err != nil {
		t.Fatal("old password lost on rollback", err)
	}
	if _, err := st.UserFromSession(ctx, oldToken, time.Hour, 24*time.Hour); err != nil {
		t.Fatal("old session lost on rollback", err)
	}
	if err := st.ResetPassword(ctx, userID, "admin-password-789", true); err == nil {
		t.Fatal("admin reset ignored audit failure")
	}
	if err := st.SetRoleSafe(ctx, userID, models.RoleViewer); err == nil {
		t.Fatal("role change ignored audit failure")
	}
	if err := st.DeleteUserSafe(ctx, userID); err == nil {
		t.Fatal("deactivation ignored audit failure")
	}
	if err := st.UpdateUserAccount(ctx, userID, "renamed", "test@example.invalid"); err == nil {
		t.Fatal("identity update ignored audit failure")
	}
	if _, err := st.CreateAccount(ctx, store.NewAccount{Username: "rejected-account", Password: "test-password-123", Role: models.RoleAdmin, MustChangePassword: true}); err == nil {
		t.Fatal("account creation ignored audit failure")
	}
	var rejectedAccounts int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE username='rejected-account'`).Scan(&rejectedAccounts); err != nil {
		t.Fatal(err)
	}
	if rejectedAccounts != 0 {
		t.Fatal("account creation was not rolled back")
	}
	user, err := st.GetUser(ctx, userID)
	if err != nil || user.Disabled || user.Role != models.RoleEditor || user.Username != "rotate-user" {
		t.Fatalf("credential changes not rolled back: user=%+v err=%v", user, err)
	}
	if _, err := pool.ExecContext(ctx, `DROP TRIGGER reject_test_audit ON audit_log`); err != nil {
		t.Fatal(err)
	}
	newToken, err := st.ChangePassword(ctx, change)
	if err != nil {
		t.Fatal(err)
	}
	if newToken == oldToken {
		t.Fatal("current bearer was not rotated")
	}
	for _, token := range []string{oldToken, otherToken} {
		if _, err := st.UserFromSession(ctx, token, time.Hour, 24*time.Hour); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("old session survived: %v", err)
		}
	}
	if _, err := st.UserFromSession(ctx, newToken, time.Hour, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AuthenticateUser(ctx, "rotate-user", change.NewPassword); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ChangePassword(ctx, change); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale request rotated credentials: %v", err)
	}
}

func TestDeactivatePreservesAuditAndRevokesAccessIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	id, err := st.CreateUser(ctx, "retired", "retire-pass-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithAuditActor(ctx, store.AuditActor{UserID: &id, Username: "retired"})
	token, err := st.CreateSession(ctx, id, time.Hour, "test", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	handle, err := st.WebauthnHandle(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetTotp(ctx, id, true, totpTestFixture); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceRecoveryCodes(ctx, id, []string{"test-hash"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWebauthnCredential(ctx, id, models.WebauthnCredential{CredentialID: []byte("credential"), PublicKey: []byte("test")}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUserSafe(ctx, id); err != nil {
		t.Fatal(err)
	}
	user, err := st.GetUser(ctx, id)
	if err != nil || !user.Disabled || user.CanWrite() || user.IsAdmin {
		t.Fatalf("offboarding state: %+v, %v", user, err)
	}
	if _, err := st.AuthenticateUser(ctx, "retired", "retire-pass-123"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("password login survived: %v", err)
	}
	if _, err := st.UserFromSession(ctx, token, time.Hour, 24*time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session survived: %v", err)
	}
	if _, err := st.CreateSession(ctx, id, time.Hour, "test", ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("disabled account got session: %v", err)
	}
	if _, err := st.UserByWebauthnHandle(ctx, handle); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("passkey lookup survived: %v", err)
	}
	if err := st.AddWebauthnCredential(ctx, id, models.WebauthnCredential{CredentialID: []byte("late"), PublicKey: []byte("test")}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("late passkey enrollment succeeded: %v", err)
	}
	if err := st.ReplaceRecoveryCodes(ctx, id, []string{"late-code"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("late recovery enrollment succeeded: %v", err)
	}
	if err := st.SetTotp(ctx, id, true, totpTestFixture); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("late TOTP enrollment succeeded: %v", err)
	}
	var auditCount, credentialCount int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE user_id=$1`, id).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 4 {
		t.Fatalf("audit attribution lost or duplicated: %d", auditCount)
	}
	if err := pool.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM sessions WHERE user_id=$1)+(SELECT count(*) FROM webauthn_credentials WHERE user_id=$1)+(SELECT count(*) FROM totp_recovery_codes WHERE user_id=$1)`, id).Scan(&credentialCount); err != nil {
		t.Fatal(err)
	}
	if credentialCount != 0 {
		t.Fatal("credentials survived deactivation")
	}
	if err := st.EnsureAdmin(ctx, "retired", "retire-pass-123", false); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureAdmin(ctx, "retired", "retire-pass-123", true); err == nil {
		t.Fatal("bootstrap reset must not reactivate a retired identity")
	}
	adminID, err := st.CreateUser(ctx, "only-admin", "admin-pass-123", models.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUserSafe(ctx, adminID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("last admin retired: %v", err)
	}
}

func TestMFAAuditAtomicIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	id, err := st.CreateUser(ctx, "mfa-user", "test-password-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithAuditActor(ctx, store.AuditActor{UserID: &id, Username: "mfa-user"})
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: id, Enabled: true, Secret: totpTestFixture, RecoveryHashes: []string{"original"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTotp(ctx, id, false, totpTestFixture); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale setup disabled active factor: %v", err)
	}
	if err := st.AddWebauthnCredential(ctx, id, models.WebauthnCredential{CredentialID: []byte("original"), PublicKey: []byte("test")}); err != nil {
		t.Fatal(err)
	}
	creds, err := st.ListWebauthnCredentials(ctx, id)
	if err != nil || len(creds) != 1 {
		t.Fatalf("initial credentials: %d %v", len(creds), err)
	}
	if _, err := pool.ExecContext(ctx, `CREATE FUNCTION reject_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test audit unavailable'; END $$;
		CREATE TRIGGER reject_test_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_test_audit()`); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: id}); err == nil {
		t.Fatal("MFA disable ignored audit failure")
	}
	if err := st.ReplaceRecoveryCodes(ctx, id, []string{"replacement"}); err == nil {
		t.Fatal("recovery regeneration ignored audit failure")
	}
	if err := st.AddWebauthnCredential(ctx, id, models.WebauthnCredential{CredentialID: []byte("replacement"), PublicKey: []byte("test")}); err == nil {
		t.Fatal("passkey enrollment ignored audit failure")
	}
	if _, err := st.DeleteWebauthnCredential(ctx, id, creds[0].ID); err == nil {
		t.Fatal("passkey removal ignored audit failure")
	}
	user, err := st.GetUser(ctx, id)
	if err != nil || !user.TotpEnabled {
		t.Fatalf("MFA disabled despite rollback: %v", err)
	}
	var originalCodes int
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM totp_recovery_codes WHERE user_id=$1 AND code_hash='original'`, id).Scan(&originalCodes); err != nil {
		t.Fatal(err)
	}
	if originalCodes != 1 {
		t.Fatal("original recovery codes lost on rollback")
	}
	remaining, err := st.ListWebauthnCredentials(ctx, id)
	if err != nil || len(remaining) != 1 || remaining[0].ID != creds[0].ID {
		t.Fatal("passkey changes not rolled back")
	}
	if _, err := pool.ExecContext(ctx, `DROP TRIGGER reject_test_audit ON audit_log`); err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteWebauthnCredential(ctx, id, creds[0].ID); err != nil {
		t.Fatal(err)
	}
	user, err = st.GetUser(ctx, id)
	if err != nil || user.TotpEnabled {
		t.Fatal("MFA disable failed")
	}
	if n, err := st.CountUnusedRecoveryCodes(ctx, id); err != nil || n != 0 {
		t.Fatalf("recovery codes remain after disable: %d %v", n, err)
	}
}

func TestAttemptReservationConcurrentIntegration(t *testing.T) {
	st, _ := scratchStore(t)
	for _, limit := range []int{5, 30} {
		var admitted atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range 40 {
			wg.Go(func() {
				<-start
				allowed, err := st.RateLimitAdmit(t.Context(), "concurrent", limit, time.Hour)
				if err != nil {
					t.Errorf("reserve: %v", err)
					return
				}
				if allowed {
					admitted.Add(1)
				}
			})
		}
		close(start)
		wg.Wait()
		if got := int(admitted.Load()); got != limit {
			t.Fatalf("admitted %d, want %d", got, limit)
		}
		if err := st.RateLimitReset(t.Context(), "concurrent"); err != nil {
			t.Fatal(err)
		}
	}
}
