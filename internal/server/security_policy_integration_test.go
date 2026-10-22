//go:build integration

package server

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

func TestRequiredPasskeyRejectsPasswordOnlyLoginIntegration(t *testing.T) {
	e := newItEnv(t)
	t.Cleanup(func() {
		if _, err := e.pool.ExecContext(e.ctx, `UPDATE security_policy SET require_mfa_admin=false,
			require_mfa_editor=false, grace_days=7, enforced_since=NULL WHERE id=1`); err != nil {
			t.Errorf("reset policy: %v", err)
		}
	})
	user, err := e.st.AuthenticateUser(e.ctx, e.uname, e.adminPass)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddWebauthnCredential(e.ctx, user.ID, models.WebauthnCredential{
		CredentialID: []byte(e.uname), PublicKey: []byte("synthetic-test-key"), Name: "Test passkey",
	}); err != nil {
		t.Fatal(err)
	}
	e.post("/admin/security-policy", url.Values{"require_admin": {"1"}, "grace_days": {"0"}})
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	resp, err := client.Get(e.srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	token := csrfRe.FindStringSubmatch(string(body))
	if token == nil {
		t.Fatal("login form lacks CSRF token")
	}
	resp, err = client.PostForm(e.srv.URL+"/login", url.Values{
		"username": {e.uname}, "password": {e.adminPass}, "csrf_token": {token[1]},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Bitte mit deinem Passkey anmelden.") {
		t.Fatal("required passkey was bypassed by password-only login")
	}
	u, err := url.Parse(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range jar.Cookies(u) {
		if cookie.Name == sessionCookie && cookie.Value != "" {
			t.Fatal("password-only login created an authenticated session")
		}
	}
}

// TestSecondFactorPolicyIntegration switches the sign-in policy on for the
// env's admin, who has no second factor: during the grace period only a banner
// appears; with no grace left every area except account self-service redirects
// to the profile, offline replays get 409, and once TOTP exists the account is
// free again but cannot drop its only factor while the policy holds.
func TestSecondFactorPolicyIntegration(t *testing.T) {
	e := newItEnv(t)
	t.Cleanup(func() {
		if _, err := e.pool.ExecContext(e.ctx, `UPDATE security_policy SET require_mfa_admin=false, require_mfa_editor=false,
			grace_days=7, enforced_since=NULL WHERE id=1`); err != nil {
			t.Errorf("reset policy: %v", err)
		}
	})
	noFollow := *e.client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	location := func(path string) (int, string) {
		t.Helper()
		resp, err := noFollow.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}

	e.post("/admin/security-policy", url.Values{"require_admin": {"1"}, "grace_days": {"7"}})
	if page := e.get("/"); !strings.Contains(page, "data-mfa-banner") || !strings.Contains(page, "ist für dein Konto eine Zwei-Faktor-Anmeldung vorgeschrieben") {
		t.Fatal("grace period must show the banner without blocking")
	}

	e.post("/admin/security-policy", url.Values{"require_admin": {"1"}, "grace_days": {"0"}})
	for _, path := range []string{"/", "/admin/users", "/bases"} {
		if status, loc := location(path); status != http.StatusSeeOther || loc != mfaSetupURL {
			t.Fatalf("GET %s: status %d location %q, want redirect to setup", path, status, loc)
		}
	}
	if status, _ := location("/profile"); status != http.StatusOK {
		t.Fatalf("profile must stay reachable, got %d", status)
	}
	if got := e.replay("/entries", url.Values{"year_id": {itoa64(e.yearID64)}}, false); got.status != http.StatusConflict {
		t.Fatalf("offline replay: status %d, want 409", got.status)
	}

	if _, err := e.pool.ExecContext(e.ctx, `UPDATE users SET totp_enabled=true WHERE username=$1`, e.uname); err != nil {
		t.Fatal(err)
	}
	if status, _ := location("/"); status != http.StatusOK {
		t.Fatalf("account with TOTP still blocked: %d", status)
	}
	page := e.post("/account/2fa/disable", url.Values{"password": {e.adminPass}, "code": {"000000"}})
	if !strings.Contains(page, "Die Anmelderichtlinie verlangt einen zweiten Faktor") {
		t.Fatal("removing the only factor must be refused while the policy holds")
	}
	var enabled bool
	if err := e.pool.QueryRowContext(e.ctx, `SELECT totp_enabled FROM users WHERE username=$1`, e.uname).Scan(&enabled); err != nil || !enabled {
		t.Fatalf("TOTP was removed: enabled=%v err=%v", enabled, err)
	}
	var audited int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log a JOIN users u ON u.id=a.user_id
		WHERE u.username=$1 AND a.action='security_policy'`, e.uname).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("policy changes audited %d times, want 2 (err=%v)", audited, err)
	}
}
