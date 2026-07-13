package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSetCookieAppliesDefaults locks the exact regression that motivated the
// cookie work: a cookie created without an explicit SameSite must still be
// emitted with SameSite=Lax (and Path=/), and Secure must follow cookieSecure.
func TestSetCookieAppliesDefaults(t *testing.T) {
	s := testServer() // CookieSecure=false, TrustProxy=false
	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)

	s.setCookie(rr, r, &http.Cookie{Name: "treckrr_session", Value: "tok", HttpOnly: true, MaxAge: 3600})

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

func TestCSVSafe(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"Hallo":       "Hallo",
		"=SUM(A1)":    "'=SUM(A1)",
		"+1+1":        "'+1+1",
		"-2":          "'-2",
		"@cmd":        "'@cmd",
		"\ttab":       "'\ttab",
		"Wiese 3,5ha": "Wiese 3,5ha",
	}
	for in, want := range cases {
		if got := csvSafe(in); got != want {
			t.Fatalf("csvSafe(%q) = %q, want %q", in, got, want)
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

// TestAuthMiddlewareNoStore ensures authenticated responses are non-cacheable.
// The header is set before the auth check, so it is present even on the
// unauthenticated redirect to /login.
func TestAuthMiddlewareNoStore(t *testing.T) {
	s := testServer()
	h := s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
