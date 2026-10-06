package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/d0linger/treckrr/internal/metrics"
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
	if c, err := s.cookie(r, loginCSRFCookie); err == nil && c.Value != "" {
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
// Every submitted csrf_token value is considered, not just the first: a stale
// session cookie used to get a session-derived token injected ahead of the login
// token, and first-value semantics then rejected every login on that browser.
func (s *Server) verifyLoginCSRF(r *http.Request) bool {
	c, err := s.cookie(r, loginCSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return anyTokenMatches(r, s.signLoginCSRF(c.Value))
}

// pending2FACSRFToken derives the CSRF token for the second login step from the
// pending-2FA cookie value (see csrfToken).
func (s *Server) pending2FACSRFToken(pending string) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("csrf2fa:" + pending))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyPending2FACSRF checks the second login step's token. /login/2fa is
// exempt from the generic middleware (see preSessionAuthPaths), so this is its
// CSRF protection: the token is bound to the HttpOnly pending-2FA cookie.
func (s *Server) verifyPending2FACSRF(r *http.Request, pending string) bool {
	if pending == "" {
		return false
	}
	return anyTokenMatches(r, s.pending2FACSRFToken(pending))
}

// anyTokenMatches reports whether the X-CSRF-Token header or any submitted
// csrf_token form value equals want. The form must already be parsed.
func anyTokenMatches(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	if got := r.Header.Get(csrfHeaderName); got != "" && hmac.Equal([]byte(got), []byte(want)) {
		return true
	}
	for _, got := range r.PostForm[csrfFieldName] {
		if got != "" && hmac.Equal([]byte(got), []byte(want)) {
			return true
		}
	}
	return false
}

// preSessionAuthPaths are the public login endpoints. Their authority never
// comes from the session cookie, and each carries its own CSRF binding instead:
// /login the seeded login token (verifyLoginCSRF), /login/2fa the token bound to
// the pending-2FA cookie (verifyPending2FACSRF), and the passkey ceremony a
// single-use server-side challenge behind a SameSite=Strict cookie plus a
// user-verified authenticator assertion. Demanding the session token there as
// well added nothing — but a revoked or expired session cookie the browser still
// held made the middleware demand a token no login page could supply, so that
// browser could not sign in again by password or passkey. Exempting these exact
// paths (rather than resolving the session in the middleware) keeps every other
// route fail-closed without a database lookup, even when that lookup fails.
var preSessionAuthPaths = map[string]bool{
	"/login":                true,
	"/login/2fa":            true,
	"/login/passkey/begin":  true,
	"/login/passkey/finish": true,
}

// skipCSRFInjectKey is the pageData flag that turns off injectCSRFField for a
// page whose POST forms embed their own token (see render).
const skipCSRFInjectKey = "SkipCSRFInject"

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
	if c, err := s.cookie(r, sessionCookie); err == nil && c.Value != "" {
		mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
		mac.Write([]byte("csrf:" + c.Value))
		return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	// Priority 2: pending 2FA step.
	if c, err := s.cookie(r, pending2FACookie); err == nil && c.Value != "" {
		return s.pending2FACSRFToken(c.Value)
	}
	return ""
}

// csrf rejects unsafe requests that lack a valid CSRF token once a session is
// established. Safe methods and pre-session requests (e.g. the login POST, which
// has no session cookie yet) pass through. The token may arrive as the
// csrf_token form field or the X-CSRF-Token header.
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The CSP report sink is exempt. The browser posts it itself, so it can
		// never carry a token, and it DOES attach the session cookie because
		// report-uri is same-origin — which made csrfToken return a token to
		// compare against and rejected every report with 403. The endpoint changes
		// no state: it reads at most 8 KiB, writes a log line and returns 204.
		if r.URL.Path == "/csp-report" || preSessionAuthPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
			if expected := s.csrfToken(r); expected != "" {
				got := r.Header.Get(csrfHeaderName)
				if got == "" {
					if err := r.ParseForm(); err != nil {
						s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
						return
					}
					if err := r.ParseMultipartForm(32 << 20); err != nil && !errors.Is(err, http.ErrNotMultipart) {
						s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
						return
					}
					got = r.FormValue(csrfFieldName)
				}
				if !hmac.Equal([]byte(got), []byte(expected)) {
					metrics.Inc(metrics.CSRFRejected)
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
