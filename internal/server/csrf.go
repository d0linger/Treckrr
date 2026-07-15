package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"regexp"
)

const (
	csrfFieldName  = "csrf_token"
	csrfHeaderName = "X-CSRF-Token"
)

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
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
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
