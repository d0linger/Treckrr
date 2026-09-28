package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/models"
)

// TestCSRFExemptsPreSessionAuthPaths pins AUTH-01: a stale session cookie must
// not make the middleware demand a session token on the login endpoints (which
// carry their own bindings), while every other route stays guarded.
func TestCSRFExemptsPreSessionAuthPaths(t *testing.T) {
	s := testServer()
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.csrf(ok)
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/login", http.StatusOK},
		{"/login/2fa", http.StatusOK},
		{"/login/passkey/begin", http.StatusOK},
		{"/login/passkey/finish", http.StatusOK},
		{"/logout", http.StatusForbidden},
		{"/entries", http.StatusForbidden},
		{"/login/other", http.StatusForbidden},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader("{}"))
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "revoked-session"})
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

// TestVerifyLoginCSRFChecksEveryValue: a second csrf_token field (e.g. one
// injected from a stale session cookie ahead of the login token) must not hide
// the valid login token.
func TestVerifyLoginCSRFChecksEveryValue(t *testing.T) {
	s := testServer()
	const seed = "login-seed"
	for _, tc := range []struct {
		name   string
		values []string
		want   bool
	}{
		{"injected token first", []string{"stale-session-token", s.signLoginCSRF(seed)}, true},
		{"login token only", []string{s.signLoginCSRF(seed)}, true},
		{"only foreign tokens", []string{"stale-session-token", "other"}, false},
		{"none", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{csrfFieldName: tc.values}
			r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: loginCSRFCookie, Value: seed})
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if got := s.verifyLoginCSRF(r); got != tc.want {
				t.Fatalf("verifyLoginCSRF = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestLogin2FARequiresPendingBoundToken: /login/2fa is exempt from the generic
// middleware, so the handler itself must reject a missing or foreign token
// before any verification work (the pending-2FA path stays CSRF-protected).
func TestLogin2FARequiresPendingBoundToken(t *testing.T) {
	s := testServer()
	pending := s.signPending2FA(7, []byte("binding"))
	withSession := httptest.NewRequest(http.MethodGet, "/", nil)
	withSession.AddCookie(&http.Cookie{Name: sessionCookie, Value: "some-session"})
	for _, tc := range []struct {
		name  string
		token string
	}{
		{"missing", ""},
		{"session-derived token", s.csrfToken(withSession)},
		{"token of another pending cookie", s.pending2FACSRFToken("other-pending")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"totp": {"123456"}}
			if tc.token != "" {
				form.Set(csrfFieldName, tc.token)
			}
			r := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(&http.Cookie{Name: pending2FACookie, Value: pending})
			rr := httptest.NewRecorder()
			s.handleLogin2FA(rr, r) // no store: must stop before touching it
			if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
				t.Fatalf("response = %d %q", rr.Code, rr.Header().Get("Location"))
			}
			if got := flashText(t, s, rr); got != "Sicherheits-Token abgelaufen. Bitte erneut versuchen." {
				t.Fatalf("flash = %q", got)
			}
		})
	}
}

// TestPending2FATokenBoundToCredentialState pins AUTH-10: a token only
// verifies against the credential state it was issued for.
func TestPending2FATokenBoundToCredentialState(t *testing.T) {
	s := testServer()
	token := s.signPending2FA(42, []byte("state-before"))
	same := func(int64) ([]byte, error) { return []byte("state-before"), nil }
	changed := func(int64) ([]byte, error) { return []byte("state-after"), nil }
	if uid, ok := s.verifyPending2FAWith(token, same); !ok || uid != 42 {
		t.Fatalf("unchanged state: uid=%d ok=%v", uid, ok)
	}
	if _, ok := s.verifyPending2FAWith(token, changed); ok {
		t.Fatal("token survived a credential-state change")
	}
}

// TestRateLimitIPNormalizesIPv6 pins AUTH-05: every address of one IPv6 /64
// shares a bucket; IPv4 and IPv4-mapped addresses keep theirs.
func TestRateLimitIPNormalizesIPv6(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"192.0.2.7", "192.0.2.7"},
		{"::ffff:192.0.2.7", "192.0.2.7"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"2001:db8:1:2::/64", "2001:db8:1:2::/64"}, // idempotent
		{"not-an-ip", "not-an-ip"},
	} {
		if got := rateLimitIP(tc.in); got != tc.want {
			t.Errorf("rateLimitIP(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if ceremonyKey("2001:db8::1") != ceremonyKey("2001:db8::ffff") {
		t.Error("ceremony key differs within one /64")
	}
	if shareKey("2001:db8::1") != shareKey("2001:db8::ffff") {
		t.Error("share key differs within one /64")
	}
}

// TestClientIPUsesEveryForwardedLine pins AUTH-06: the right-most entry across
// ALL X-Forwarded-For lines wins, and it must be a valid IP.
func TestClientIPUsesEveryForwardedLine(t *testing.T) {
	_, net10, _ := net.ParseCIDR("10.0.0.0/8")
	s := &Server{cfg: &config.Config{TrustProxy: true, TrustedProxies: []*net.IPNet{net10}}}
	for _, tc := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"proxy appends its own line", []string{"1.2.3.4", "203.0.113.9"}, "203.0.113.9"},
		{"merged single line", []string{"1.2.3.4, 203.0.113.9"}, "203.0.113.9"},
		{"trailing empty entry", []string{"203.0.113.9, "}, "203.0.113.9"},
		{"ipv6 canonicalized", []string{"2001:DB8::0001"}, "2001:db8::1"},
		{"entry with port", []string{"203.0.113.9:4711"}, "203.0.113.9"},
		{"garbage falls back to peer", []string{"1.2.3.4", "not-an-ip"}, "10.1.2.3"},
		{"empty header falls back to peer", []string{""}, "10.1.2.3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = "10.1.2.3:5555"
			for _, line := range tc.lines {
				r.Header.Add("X-Forwarded-For", line)
			}
			if got := s.clientIP(r); got != tc.want {
				t.Fatalf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSafeReturnPathRejectsControlCharacters pins AUTH-11.
func TestSafeReturnPathRejectsControlCharacters(t *testing.T) {
	for _, tc := range []struct{ referer, want string }{
		{"http://localhost:8080/%09/evil.com", "/fallback"},
		{"http://localhost:8080/%0a/evil.com", "/fallback"},
		{"http://localhost:8080/x?a=%09", "/x?a=%09"},
		{"http://localhost:8080/ok%20path?q=1", "/ok%20path?q=1"},
		{"http://localhost:8080/%2F/evil.com", "/fallback"},
		{"http://localhost:8080/profile", "/profile"},
	} {
		r := httptest.NewRequest(http.MethodGet, "http://localhost:8080/theme", nil)
		r.Header.Set("Referer", tc.referer)
		if got := safeReturnPath(r, "/fallback"); got != tc.want {
			t.Errorf("safeReturnPath(%q) = %q, want %q", tc.referer, got, tc.want)
		}
	}
}

// TestOfflineReplayAuthBranches pins the server half of SYN-11: a replayed
// submission gets a status it can act on instead of a 303.
func TestOfflineReplayAuthBranches(t *testing.T) {
	s := testServer()
	reached := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	viewer := &models.User{ID: 1, Username: "v", Role: models.RoleViewer}
	mustChange := &models.User{ID: 2, Username: "m", Role: models.RoleEditor, MustChangePassword: true}
	adminMustChange := &models.User{ID: 3, Username: "a", Role: models.RoleAdmin, IsAdmin: true, MustChangePassword: true}
	for _, tc := range []struct {
		name   string
		wrap   func(http.HandlerFunc) http.Handler
		user   *models.User
		replay bool
		want   int
	}{
		{"viewer replay", s.auth, viewer, true, http.StatusForbidden},
		{"viewer navigation", s.auth, viewer, false, http.StatusSeeOther},
		{"must change replay", s.auth, mustChange, true, http.StatusConflict},
		{"must change navigation", s.auth, mustChange, false, http.StatusSeeOther},
		{"admin must change replay", s.admin, adminMustChange, true, http.StatusConflict},
		{"admin must change navigation", s.admin, adminMustChange, false, http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/entries", strings.NewReader(""))
			r = r.WithContext(context.WithValue(r.Context(), userCacheKey, &userCache{user: tc.user, done: true}))
			if tc.replay {
				r.Header.Set("X-Offline-Replay", "1")
			}
			rr := httptest.NewRecorder()
			tc.wrap(reached).ServeHTTP(rr, r)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
			if tc.replay && rr.Header().Get("Location") != "" {
				t.Fatalf("replay got a redirect to %q", rr.Header().Get("Location"))
			}
			if tc.replay && len(rr.Header().Values("Set-Cookie")) != 0 {
				t.Fatalf("replay set cookies (e.g. a spurious flash): %v", rr.Header().Values("Set-Cookie"))
			}
		})
	}
}

// TestStaleSessionCookieExpiredOnlyWhenGone: auth() drops a cookie the store
// positively no longer knows, but keeps it through a transient lookup failure.
func TestStaleSessionCookieExpiredOnlyWhenGone(t *testing.T) {
	s := testServer()
	h := s.auth(func(http.ResponseWriter, *http.Request) {})
	for _, tc := range []struct {
		name    string
		stale   bool
		cleared bool
	}{
		{"revoked session", true, true},
		{"transient failure", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "token"})
			r = r.WithContext(context.WithValue(r.Context(), userCacheKey, &userCache{stale: tc.stale, done: true}))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, r)
			if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
				t.Fatalf("response = %d %q", rr.Code, rr.Header().Get("Location"))
			}
			_, has := responseCookiesByName(t, rr)[sessionCookie]
			if has != tc.cleared {
				t.Fatalf("session cookie cleared = %v, want %v", has, tc.cleared)
			}
			if tc.cleared {
				requireClearedCookie(t, responseCookiesByName(t, rr), sessionCookie)
			}
		})
	}
}
