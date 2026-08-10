package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"regexp"
	"time"
)

const (
	csrfFieldName  = "csrf_token"
	csrfHeaderName = "X-CSRF-Token"
	// loginCSRFCookie holds a random signing seed for the pre-session login form.
	loginCSRFCookie = "treckrr_lcsrf"
)

// loginCSRFToken returns the CSRF token for the pre-session login form, setting a
// fresh random signing-seed cookie when none is present. POST /login has no
// session yet, so the general csrf()/csrfToken() path can't protect it; this
// seeded double-submit token closes the login-CSRF gap (an attacker can't read
// the HttpOnly seed cookie, so can't forge a matching token).
func (s *Server) loginCSRFToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(loginCSRFCookie); err == nil && c.Value != "" {
		return s.signLoginCSRF(c.Value)
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	seed := base64.RawURLEncoding.EncodeToString(b)
	s.setCookie(w, r, &http.Cookie{Name: loginCSRFCookie, Value: seed, MaxAge: int((30 * time.Minute).Seconds())})
	return s.signLoginCSRF(seed)
}

func (s *Server) signLoginCSRF(seed string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("csrflogin:" + seed))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyLoginCSRF checks the submitted login-form token against the seed cookie.
func (s *Server) verifyLoginCSRF(r *http.Request) bool {
	c, err := r.Cookie(loginCSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.FormValue(csrfFieldName)
	return got != "" && hmac.Equal([]byte(got), []byte(s.signLoginCSRF(c.Value)))
}

// formOpenTag matches an HTML <form ...> opening tag that submits via POST.
// [^>] also matches newlines, so multi-line form tags are handled; method="get"
// and method="dialog" forms are deliberately left untouched.
var formOpenTag = regexp.MustCompile(`(?i)<form\b[^>]*\bmethod\s*=\s*["']post["'][^>]*>`)

// csrfToken derives a stateless CSRF token by signing the value of either the
// session cookie or the pending-2FA cookie with the app secret. An attacker
// cannot read the HttpOnly cookies, so cannot compute a matching token for a
// cross-site request. Returns "" when no qualifying cookie is present.
func (s *Server) csrfToken(r *http.Request) string {
	// Priority 1: established session.
	if c, err := r.Cookie(s.sessionCookieName(r)); err == nil && c.Value != "" {
		mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
		mac.Write([]byte("csrf:" + c.Value))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	// Priority 2: pending 2FA step.
	if c, err := r.Cookie(pending2FACookie); err == nil && c.Value != "" {
		mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
		mac.Write([]byte("csrf2fa:" + c.Value))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	return ""
}

// csrf rejects unsafe requests that lack a valid CSRF token once a session is
// established. Safe methods and pre-session requests (e.g. the login POST, which
// has no session cookie yet) pass through. The token may arrive as the
// csrf_token form field or the X-CSRF-Token header.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if expected := s.csrfToken(r); expected != "" {
				got := r.Header.Get(csrfHeaderName)
				if got == "" {
					got = r.FormValue(csrfFieldName)
				}
				if !hmac.Equal([]byte(got), []byte(expected)) {
					http.Error(w, "CSRF-Token ungültig oder fehlt. Bitte die Seite neu laden.", http.StatusForbidden)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// injectCSRFField inserts a hidden CSRF input immediately after every POST
// <form> opening tag in the already-rendered HTML, so no template needs to know
// about the token. A no-op when there is no session token.
func injectCSRFField(html []byte, token string) []byte {
	if token == "" {
		return html
	}
	field := []byte(`<input type="hidden" name="` + csrfFieldName + `" value="` + token + `">`)
	return formOpenTag.ReplaceAllFunc(html, func(tag []byte) []byte {
		out := make([]byte, 0, len(tag)+len(field))
		out = append(out, tag...)
		out = append(out, field...)
		return out
	})
}
