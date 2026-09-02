package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSameSiteStrictForShortLivedCookies ensures that the transitional short-lived cookies
// (pending2FACookie and waCookie) are set with SameSite=Strict to protect against login-flow CSRF.
func TestSameSiteStrictForShortLivedCookies(t *testing.T) {
	s := testServer()

	// Test pending2FACookie
	rr1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/", nil)
	s.setCookie(rr1, r1, &http.Cookie{
		Name:     pending2FACookie,
		Value:    "test-val",
		SameSite: http.SameSiteStrictMode,
	})
	sc1 := rr1.Header().Get("Set-Cookie")
	if !strings.Contains(sc1, "SameSite=Strict") {
		t.Fatalf("expected SameSite=Strict for pending2FACookie, got %q", sc1)
	}

	// Test waCookie
	rr2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/", nil)
	s.setCookie(rr2, r2, &http.Cookie{
		Name:     waCookie,
		Value:    "test-val",
		SameSite: http.SameSiteStrictMode,
	})
	sc2 := rr2.Header().Get("Set-Cookie")
	if !strings.Contains(sc2, "SameSite=Strict") {
		t.Fatalf("expected SameSite=Strict for waCookie, got %q", sc2)
	}
}

// TestSetCookieAppliesDefaults locks the exact regression that motivated the
// cookie work: a cookie created without an explicit SameSite must still be
// emitted with SameSite=Lax (and Path=/), and Secure must follow cookieSecure.
func TestSetCookieAppliesDefaults(t *testing.T) {
	s := testServer() // CookieSecure=false, TrustProxy=false
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	// Explicit HttpOnly:false must not weaken the guarantee — setCookie forces it on.
	s.setCookie(rr, r, &http.Cookie{Name: "treckrr_session", Value: "tok", HttpOnly: false, MaxAge: 3600})

	sc := rr.Header().Get("Set-Cookie")
	if !strings.Contains(sc, "SameSite=Lax") {
		t.Fatalf("expected SameSite=Lax in %q", sc)
	}
	if !strings.Contains(sc, "Path=/") {
		t.Fatalf("expected Path=/ in %q", sc)
	}
	if !strings.Contains(sc, "HttpOnly") {
		t.Fatalf("expected HttpOnly in %q", sc)
	}
	if strings.Contains(sc, "Secure") {
		t.Fatalf("Secure must be absent over plain HTTP: %q", sc)
	}
}

// TestHostPrefixAppliedToEveryCookie locks the cookie-tossing defense: over HTTPS
// every cookie carries __Host-, so a sibling host under the same registrable
// domain cannot overwrite one (it would have to set Domain, which the prefix
// forbids). Over plain HTTP the prefix must be absent — browsers reject __Host-
// cookies without Secure, which would break local dev entirely.
func TestHostPrefixAppliedToEveryCookie(t *testing.T) {
	names := []string{sessionCookie, flashCookie, loginCSRFCookie, pending2FACookie, shareOnceCookie, waCookie, themeCookie}

	secure := testServer()
	secure.cfg.CookieSecure = true
	for _, name := range names {
		rr := httptest.NewRecorder()
		secure.setCookie(rr, httptest.NewRequest(http.MethodGet, "/", nil), &http.Cookie{Name: name, Value: "v"})
		sc := rr.Header().Get("Set-Cookie")
		if !strings.HasPrefix(sc, hostCookiePrefix+name+"=") {
			t.Errorf("%s over HTTPS: want %s prefix, got %q", name, hostCookiePrefix, sc)
		}
		// __Host- is only honored with Secure + Path=/ + no Domain.
		if !strings.Contains(sc, "Secure") || !strings.Contains(sc, "Path=/") || strings.Contains(sc, "Domain=") {
			t.Errorf("%s: __Host- requirements not met: %q", name, sc)
		}
	}

	plain := testServer() // CookieSecure=false, TrustProxy=false
	for _, name := range names {
		rr := httptest.NewRecorder()
		plain.setCookie(rr, httptest.NewRequest(http.MethodGet, "/", nil), &http.Cookie{Name: name, Value: "v"})
		if sc := rr.Header().Get("Set-Cookie"); !strings.HasPrefix(sc, name+"=") {
			t.Errorf("%s over plain HTTP: prefix must be absent, got %q", name, sc)
		}
	}
}

// A cookie written with the prefix must be found by the matching read helper —
// the two halves have to agree or every cookie silently stops round-tripping.
func TestCookieReadMatchesWrittenName(t *testing.T) {
	for _, cookieSecure := range []bool{false, true} {
		s := testServer()
		s.cfg.CookieSecure = cookieSecure

		rr := httptest.NewRecorder()
		s.setCookie(rr, httptest.NewRequest(http.MethodGet, "/", nil), &http.Cookie{Name: flashCookie, Value: "abc"})
		c, err := http.ParseSetCookie(rr.Header().Get("Set-Cookie"))
		if err != nil {
			t.Fatalf("parse Set-Cookie: %v", err)
		}

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		got, err := s.cookie(r, flashCookie)
		if err != nil {
			t.Fatalf("cookieSecure=%v: written %q not found on read back: %v", cookieSecure, c.Name, err)
		}
		if got.Value != "abc" {
			t.Errorf("cookieSecure=%v: value = %q, want abc", cookieSecure, got.Value)
		}
	}
}

func TestCSVSafe(t *testing.T) {
	// Construct non-ASCII whitespace from code points so the source stays ASCII.
	nbsp := string(rune(0x00A0)) // NO-BREAK SPACE
	emsp := string(rune(0x2003)) // EM SPACE

	cases := []struct{ in, want string }{
		{"", ""},
		{"Hallo", "Hallo"},
		{"=SUM(A1)", "'=SUM(A1)"},
		{"+1+1", "'+1+1"},
		{"-2", "'-2"},
		{"@cmd", "'@cmd"},
		{"%cmd", "'%cmd"},
		{"|cmd", "'|cmd"},
		{"\t=cmd", "'\t=cmd"}, // leading tab then a trigger -> quoted
		{" =1+1", "' =1+1"},   // leading ASCII space -> quoted, original kept
		{nbsp + "=SUM(A1)", "'" + nbsp + "=SUM(A1)"}, // NBSP prefix -> quoted
		{emsp + "=1+1", "'" + emsp + "=1+1"},         // em-space prefix -> quoted
		{"   ", "   "},                               // ASCII whitespace only -> unchanged
		{nbsp + emsp, nbsp + emsp},                   // Unicode whitespace only -> unchanged
		{"Wert =x", "Wert =x"},                       // trigger not first non-space -> unchanged
		{"\ttab", "\ttab"},                           // leading tab then plain text -> unchanged
		{"Wiese 3,5ha", "Wiese 3,5ha"},
	}
	for _, c := range cases {
		if got := csvSafe(c.in); got != c.want {
			t.Fatalf("csvSafe(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSecurityHeaders locks the hardening headers set on every response.
func TestSecurityHeaders(t *testing.T) {
	s := testServer()
	h := s.securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rr.Header().Get("X-XSS-Protection"); got != "0" {
		t.Errorf("X-XSS-Protection = %q, want 0", got)
	}
	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestAuthMiddlewareNoStore ensures both authenticated-area middlewares mark
// responses non-cacheable. The header is set as the first statement of each
// wrapper — before the auth check — so an unauthenticated request through
// either one already carries it. (A genuinely authenticated pass-through would
// need a DB-backed session, which these unit tests deliberately avoid; it runs
// the identical header code either way.)
func TestAuthMiddlewareNoStore(t *testing.T) {
	s := testServer()
	noop := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	for name, h := range map[string]http.Handler{
		"auth":  s.auth(noop),
		"admin": s.admin(noop),
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s middleware: Cache-Control = %q, want no-store", name, got)
		}
	}
}
