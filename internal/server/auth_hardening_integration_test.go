//go:build integration

package server

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/backup"
	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/totp"
)

// hardeningEnv is a full HTTP stack (real middleware chain, templates and
// WebAuthn config) over an isolated scratch database, driven with explicit
// cookies so a test can keep presenting a cookie a real browser would hold.
type hardeningEnv struct {
	t    *testing.T
	srv  *Server
	st   *store.Store
	pool *sql.DB
	ts   *httptest.Server
	c    *http.Client
}

func newHardeningEnv(t *testing.T) *hardeningEnv {
	t.Helper()
	st, pool := securityTestStore(t)
	cfg := &config.Config{
		SessionSecret:    "test-session-secret-at-least-32-bytes!!",
		EncryptionSecret: "test-session-secret-at-least-32-bytes!!",
		AdminUsername:    "itadmin",
		RPID:             "localhost",
		RPOrigin:         "http://localhost",
	}
	srv, err := New(cfg, st, backup.New(backup.Options{}, pool))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &hardeningEnv{t: t, srv: srv, st: st, pool: pool, ts: ts, c: &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// hResp is the part of a response the tests inspect; the body is already read
// and closed.
type hResp struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func (r *hResp) Cookies() []*http.Cookie { return r.cookies }

// do sends one request with exactly the given cookies and returns the
// response plus its body text.
func (e *hardeningEnv) do(method, path, contentType, body string, header http.Header, cookies ...*http.Cookie) (*hResp, string) {
	e.t.Helper()
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	resp, err := e.c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &hResp{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}, string(b)
}

func (e *hardeningEnv) postForm(path string, form url.Values, header http.Header, cookies ...*http.Cookie) (*hResp, string) {
	e.t.Helper()
	return e.do(http.MethodPost, path, "application/x-www-form-urlencoded", form.Encode(), header, cookies...)
}

// user creates an editor that can log in without a forced password change.
func (e *hardeningEnv) user(name, password string) int64 {
	e.t.Helper()
	id, err := e.st.CreateUser(e.t.Context(), name, password, models.RoleEditor)
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// session opens a server-side session and returns its cookie plus the CSRF
// header value a page rendered for it would carry.
func (e *hardeningEnv) session(userID int64) (*http.Cookie, http.Header) {
	e.t.Helper()
	token, err := e.st.CreateSession(e.t.Context(), userID, sessionTTL, "test", "127.0.0.1")
	if err != nil {
		e.t.Fatal(err)
	}
	c := &http.Cookie{Name: sessionCookie, Value: token}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(c)
	return c, http.Header{csrfHeaderName: {e.srv.csrfToken(r)}}
}

// enableTOTP turns on TOTP with a fresh seed and one known recovery code.
func (e *hardeningEnv) enableTOTP(userID int64) (secret, recovery string) {
	e.t.Helper()
	secret, err := totp.GenerateSecret()
	if err != nil {
		e.t.Fatal(err)
	}
	plain, hashes, err := auth.GenerateRecoveryCodes(2)
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.st.ConfigureTwoFactor(e.t.Context(), store.TwoFactorChange{
		UserID: userID, Enabled: true, Secret: secret, RecoveryHashes: hashes,
	}); err != nil {
		e.t.Fatal(err)
	}
	return secret, plain[0]
}

func cookieNamed(resp *hResp, name string) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func requireExpired(t *testing.T, resp *hResp, name string) {
	t.Helper()
	c := cookieNamed(resp, name)
	if c == nil || c.Value != "" || c.MaxAge >= 0 {
		t.Fatalf("cookie %q not expired: %#v", name, c)
	}
}

var csrfFieldValueRe = regexp.MustCompile(`name="csrf_token" value="([^"]*)"`)

// formTokens returns every csrf_token hidden-field value on a page.
func formTokens(body string) []string {
	var out []string
	for _, m := range csrfFieldValueRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// TestLoginWithRevokedSessionCookieIntegration pins AUTH-01: a browser still
// holding a revoked session cookie can sign in again by password and by
// passkey, and the dead cookie is expired along the way.
func TestLoginWithRevokedSessionCookieIntegration(t *testing.T) {
	e := newHardeningEnv(t)
	const password = "Stale-password-123"
	uid := e.user("stale-cookie-user", password)
	stale, _ := e.session(uid)
	if err := e.st.DeleteUserSessionsExcept(t.Context(), uid, ""); err != nil {
		t.Fatal(err)
	}

	resp, _ := e.do(http.MethodGet, "/", "", "", nil, stale)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" {
		t.Fatalf("GET / = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	requireExpired(t, resp, sessionCookie)

	resp, body := e.do(http.MethodGet, "/login", "", "", nil, stale)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /login = %d", resp.StatusCode)
	}
	requireExpired(t, resp, sessionCookie)
	tokens := formTokens(body)
	if len(tokens) != 1 {
		t.Fatalf("login form carries %d csrf_token fields, want exactly its own: %v", len(tokens), tokens)
	}
	seed := cookieNamed(resp, loginCSRFCookie)
	if seed == nil {
		t.Fatal("login page set no login-CSRF seed")
	}

	// The browser may keep sending the dead cookie (e.g. a second tab raced the
	// expiry); the password login must still go through.
	resp, _ = e.postForm("/login", url.Values{
		"username": {"stale-cookie-user"}, "password": {password}, csrfFieldName: {tokens[0]},
	}, nil, stale, seed)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("password login with stale cookie = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	fresh := cookieNamed(resp, sessionCookie)
	if fresh == nil || fresh.Value == "" || fresh.Value == stale.Value {
		t.Fatalf("no fresh session issued: %#v", fresh)
	}

	// passkey.js sends begin without any CSRF header.
	resp, body = e.do(http.MethodPost, "/login/passkey/begin", "application/json", "{}", nil, stale)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "publicKey") {
		t.Fatalf("passkey begin with stale cookie = %d %s", resp.StatusCode, body)
	}
}

// TestLoginSuccessKeepsSharedIPFailuresIntegration pins AUTH-04: a successful
// login from an address no longer wipes the failures other accounts collected
// from it.
func TestLoginSuccessKeepsSharedIPFailuresIntegration(t *testing.T) {
	st, pool := securityTestStore(t)
	if _, err := st.CreateUser(t.Context(), "own-account", "Own-password-123", models.RoleEditor); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret"}, store: st, logins: newLoginLimiter(st)}
	attempt := func(username, password string) {
		const seed = "spray-seed"
		form := url.Values{"username": {username}, "password": {password}, csrfFieldName: {s.signLoginCSRF(seed)}}
		r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode())).WithContext(t.Context())
		r.RemoteAddr = "192.0.2.50:4444"
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(&http.Cookie{Name: loginCSRFCookie, Value: seed})
		s.csrf(http.HandlerFunc(s.handleLogin)).ServeHTTP(httptest.NewRecorder(), r)
	}
	for i := range loginMaxFails - 1 {
		attempt("victim-"+itoa(i), "guess")
	}
	attempt("own-account", "Own-password-123") // success must not launder the misses
	attempt("victim-x", "guess")               // the fifth miss is still admitted
	attempt("victim-y", "guess")               // ...and now the address is blocked
	var failed, blocked, success int
	if err := pool.QueryRowContext(t.Context(), `SELECT
		count(*) FILTER (WHERE action='login_failed'),
		count(*) FILTER (WHERE action='login_blocked'),
		(SELECT count(*) FROM sessions)
		FROM audit_log`).Scan(&failed, &blocked, &success); err != nil {
		t.Fatal(err)
	}
	if success != 1 || failed != loginMaxFails || blocked != 1 {
		t.Fatalf("success=%d failed=%d blocked=%d, want 1/%d/1", success, failed, blocked, loginMaxFails)
	}
}

// TestPasskeyEnrollmentRequiresSecondFactorIntegration pins AUTH-03: with TOTP
// on, enrolling a passkey needs a fresh second factor on top of the password.
func TestPasskeyEnrollmentRequiresSecondFactorIntegration(t *testing.T) {
	e := newHardeningEnv(t)
	const password = "Enroll-password-123"
	uid := e.user("enroll-user", password)
	secret, _ := e.enableTOTP(uid)
	sess, hdr := e.session(uid)
	begin := func(body string) (int, string) {
		resp, text := e.do(http.MethodPost, "/account/passkeys/register/begin", "application/json", body, hdr, sess)
		return resp.StatusCode, text
	}
	if code, text := begin(`{"password":"` + password + `"}`); code != http.StatusForbidden || !strings.Contains(text, "Zwei‑Faktor‑Code falsch") {
		t.Fatalf("password only = %d %q", code, text)
	}
	if code, _ := begin(`{"password":"` + password + `","code":"000000x"}`); code != http.StatusForbidden {
		t.Fatalf("wrong code = %d", code)
	}
	current, _, err := totp.CodeAt(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := begin(`{"password":"wrong-password","code":"` + current + `"}`); code != http.StatusForbidden {
		t.Fatalf("wrong password = %d", code)
	}
	if code, text := begin(`{"password":"` + password + `","code":"` + current + `"}`); code != http.StatusOK || !strings.Contains(text, "publicKey") {
		t.Fatalf("password + code = %d %s", code, text)
	}
	if code, _ := begin(`{"password":"` + password + `","code":"` + current + `"}`); code != http.StatusForbidden {
		t.Fatalf("replayed code = %d, want it consumed", code)
	}
	var denied int
	if err := e.pool.QueryRowContext(t.Context(), `SELECT count(*) FROM audit_log WHERE action='passkey_add_denied'`).Scan(&denied); err != nil {
		t.Fatal(err)
	}
	if denied != 4 {
		t.Fatalf("passkey_add_denied audits = %d, want 4", denied)
	}
}

// TestTwoFactorDisableRequiresSecondFactorIntegration pins the AUTH-03
// disable path: the password alone no longer removes TOTP.
func TestTwoFactorDisableRequiresSecondFactorIntegration(t *testing.T) {
	e := newHardeningEnv(t)
	const password = "Disable-password-123"
	uid := e.user("disable-user", password)
	_, recovery := e.enableTOTP(uid)
	sess, hdr := e.session(uid)
	enabled := func() bool {
		u, err := e.st.GetUser(t.Context(), uid)
		if err != nil {
			t.Fatal(err)
		}
		return u.TotpEnabled
	}
	resp, _ := e.postForm("/account/2fa/disable", url.Values{"password": {password}}, hdr, sess)
	if resp.StatusCode != http.StatusSeeOther || !enabled() {
		t.Fatalf("password-only disable: status %d, still enabled %v", resp.StatusCode, enabled())
	}
	resp, _ = e.postForm("/account/2fa/disable", url.Values{"password": {password}, "code": {recovery}}, hdr, sess)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/profile" || enabled() {
		t.Fatalf("disable with recovery code: %d %q, still enabled %v", resp.StatusCode, resp.Header.Get("Location"), enabled())
	}
}

var totpSecretRe = regexp.MustCompile(`id="totp-secret">([A-Z2-7]+)<`)

// TestTwoFactorSetupSeedAndEnrollmentCodeIntegration pins AUTH-07 (a fresh
// seed per setup visit, kept only across a failed confirmation) and AUTH-08
// (the enrollment code is consumed and cannot complete a login).
func TestTwoFactorSetupSeedAndEnrollmentCodeIntegration(t *testing.T) {
	e := newHardeningEnv(t)
	const password = "Setup-password-123"
	uid := e.user("setup-user", password)
	sess, hdr := e.session(uid)
	seed := func(path string) string {
		resp, body := e.do(http.MethodGet, path, "", "", nil, sess)
		m := totpSecretRe.FindStringSubmatch(body)
		if resp.StatusCode != http.StatusOK || m == nil {
			t.Fatalf("GET %s = %d, no secret", path, resp.StatusCode)
		}
		return m[1]
	}
	first := seed("/account/2fa")
	second := seed("/account/2fa")
	if first == second {
		t.Fatal("setup page reused the pending seed of an earlier visit")
	}
	if retry := seed(twoFactorRetryPath); retry != second {
		t.Fatal("retry after a failed confirmation lost the scanned seed")
	}

	code, step, err := totp.CodeAt(second, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, body := e.postForm("/account/2fa/confirm", url.Values{"code": {code}, "password": {password}}, hdr, sess)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "data-codes") {
		t.Fatalf("confirm = %d", resp.StatusCode)
	}
	var lastStep sql.NullInt64
	if err := e.pool.QueryRowContext(t.Context(), `SELECT totp_last_step FROM users WHERE id=$1`, uid).Scan(&lastStep); err != nil {
		t.Fatal(err)
	}
	// ValidateStep may match the neighboring step when the clock ticks over.
	if !lastStep.Valid || lastStep.Int64 < int64(step)-1 || lastStep.Int64 > int64(step)+1 {
		t.Fatalf("totp_last_step = %v, want the enrollment step %d", lastStep, step)
	}

	// Replaying the enrollment code at the second login step must fail.
	resp, body = e.do(http.MethodGet, "/login", "", "", nil)
	loginSeed := cookieNamed(resp, loginCSRFCookie)
	resp, _ = e.postForm("/login", url.Values{
		"username": {"setup-user"}, "password": {password}, csrfFieldName: formTokens(body),
	}, nil, loginSeed)
	pending := cookieNamed(resp, pending2FACookie)
	if pending == nil || pending.Value == "" {
		t.Fatalf("password step issued no pending-2FA cookie (status %d)", resp.StatusCode)
	}
	resp, body = e.do(http.MethodGet, "/login", "", "", nil, pending, loginSeed)
	tokens := formTokens(body)
	if resp.StatusCode != http.StatusOK || len(tokens) != 1 || !strings.Contains(body, `action="/login/2fa"`) {
		t.Fatalf("2FA step form tokens = %v", tokens)
	}
	resp, _ = e.postForm("/login/2fa", url.Values{"totp": {code}, csrfFieldName: tokens}, nil, pending, loginSeed)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/login" || cookieNamed(resp, sessionCookie) != nil {
		t.Fatalf("replayed enrollment code logged in: %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
}

// TestPendingTwoFactorTokenInvalidatedIntegration pins AUTH-10: a pending-2FA
// token dies with any change to the credential state it was issued against.
func TestPendingTwoFactorTokenInvalidatedIntegration(t *testing.T) {
	st, _ := securityTestStore(t)
	ctx := t.Context()
	uid, err := st.CreateUser(ctx, "pending-user", "Pending-password-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	plain, hashes, err := auth.GenerateRecoveryCodes(2)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: uid, Enabled: true, Secret: "JBSWY3DPEHPK3PXP", RecoveryHashes: hashes}); err != nil { // #nosec G101 -- public test seed, not a credential
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret"}, store: st}
	issue := func() string {
		b, err := st.PendingTwoFactorBinding(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		return s.signPending2FA(uid, b)
	}
	for _, tc := range []struct {
		name   string
		change func() error
	}{
		{"completed TOTP step", func() error { _, err := st.AcceptTotpStep(ctx, uid, 1000); return err }},
		{"consumed recovery code", func() error { _, err := st.ConsumeRecoveryCode(ctx, uid, auth.HashRecoveryCode(plain[0])); return err }},
		{"admin password reset", func() error { return st.ResetPassword(ctx, uid, "Reset-password-456", false) }},
		{"admin 2FA reset", func() error { return st.ResetTotpForUser(ctx, uid) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			token := issue()
			if got, ok := s.verifyPending2FA(ctx, token); !ok || got != uid {
				t.Fatalf("fresh token rejected: %d %v", got, ok)
			}
			if err := tc.change(); err != nil {
				t.Fatal(err)
			}
			if _, ok := s.verifyPending2FA(ctx, token); ok {
				t.Fatal("pending-2FA token survived the credential change")
			}
		})
	}
}

// TestPasskeyCloneWarningRefusesLoginIntegration pins AUTH-09: a counter that
// did not advance — flagged by go-webauthn or lost in a race — refuses the
// login instead of only logging it.
func TestPasskeyCloneWarningRefusesLoginIntegration(t *testing.T) {
	st, pool := securityTestStore(t)
	ctx := t.Context()
	uid, err := st.CreateUser(ctx, "clone-user", "Clone-password-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.GetUser(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	credID := []byte("clone-credential")
	if err := st.AddWebauthnCredential(ctx, uid, models.WebauthnCredential{
		CredentialID: credID, PublicKey: []byte("pk"), AAGUID: make([]byte, 16), SignCount: 10, Name: "Key",
	}); err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret"}, store: st, logins: newLoginLimiter(st)}
	finish := func(count uint32, cloneWarning bool) *httptest.ResponseRecorder {
		cred := &webauthn.Credential{ID: credID, Authenticator: webauthn.Authenticator{SignCount: count, CloneWarning: cloneWarning}}
		r := httptest.NewRequest(http.MethodPost, "/login/passkey/finish", nil).WithContext(ctx)
		rr := httptest.NewRecorder()
		s.completePasskeyLogin(rr, r, user, cred, "192.0.2.77")
		return rr
	}
	for _, tc := range []struct {
		name         string
		count        uint32
		cloneWarning bool
		want         int
	}{
		{"library clone warning", 10, true, http.StatusUnauthorized},
		{"lost race / regression", 9, false, http.StatusUnauthorized},
		{"advancing counter", 11, false, http.StatusOK},
		{"replayed count", 11, false, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := finish(tc.count, tc.cloneWarning)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
			_, hasSession := responseCookiesByName(t, rr)[sessionCookie]
			if hasSession != (tc.want == http.StatusOK) {
				t.Fatalf("session issued = %v", hasSession)
			}
		})
	}
	var warnings int
	var stored int64
	if err := pool.QueryRowContext(ctx, `SELECT count(*) FROM audit_log WHERE action='login_passkey_clone_warning'`).Scan(&warnings); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT sign_count FROM webauthn_credentials WHERE credential_id=$1`, credID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if warnings != 3 || stored != 11 {
		t.Fatalf("clone warnings = %d, stored count = %d; want 3 and 11", warnings, stored)
	}
}
