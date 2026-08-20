package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/totp"
)

const (
	pending2FACookie = "treckrr_2fa"
	pending2FATTL    = 5 * time.Minute
)

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.currentUser(r) != nil {
		redirect(w, r, "/")
		return
	}
	// "Abbrechen" from the 2FA step clears the pending state.
	if r.URL.Query().Get("cancel") == "1" {
		s.clearPending2FA(w, r)
		redirect(w, r, "/login")
		return
	}
	data := pageData{"Title": "Anmelden", "Theme": themeFromCookie(r), "CSRF": s.loginCSRFToken(w, r)}
	// If a valid pending-2FA cookie is present, show the second step instead.
	if c, err := r.Cookie(pending2FACookie); err == nil {
		if _, ok := s.verifyPending2FA(c.Value); ok {
			data["ShowTotp"] = true
		}
	}
	if msg, kind, _ := s.readFlash(w, r); msg != "" {
		data["FlashMessage"] = msg
		data["FlashKind"] = kind
	}
	s.render(w, r, "login", data)
}

// handleLogin is step 1: verify username + password. If the account has 2FA it
// stores a short-lived signed pending token and shows the code step; otherwise
// it establishes the session directly.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	// POST /login has no session yet, so the general csrf() middleware can't guard
	// it; verify the seeded login-CSRF token to block login-CSRF (an attacker
	// silently signing the victim into the attacker's account).
	if !s.verifyLoginCSRF(r) {
		s.setFlash(w, r, "error", "Sicherheits-Token abgelaufen. Bitte erneut anmelden.")
		redirect(w, r, "/login")
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	rlKey := s.clientIP(r)

	// Only apply the account-scoped limiter to plausibly-real usernames: an
	// over-long value can never match an account (AuthenticateUser rejects it),
	// so skipping it stops a flood of junk usernames from writing oversized
	// rate-limit keys. Stale keys are evicted by PurgeStaleRateLimits.
	accountLimited := username != "" && utf8.RuneCountInString(username) <= maxUsernameLen

	// Throttle by source IP AND by target account: the account-scoped limit
	// bounds a distributed (many-IP) guessing campaign against one username,
	// which the per-IP limit alone cannot. NOTE: a temporary account block is a
	// deliberate trade-off; it is time-bounded and self-healing, and passkey
	// login (a separate route) is unaffected, so it is not an unrecoverable lockout.
	if s.logins.blocked(r.Context(), rlKey) || (accountLimited && s.logins.accountBlocked(r.Context(), username)) {
		s.auditLogin(r, username, "login_blocked", "zu viele Fehlversuche")
		s.setFlash(w, r, "error", "Zu viele Fehlversuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, "/login")
		return
	}

	user, err := s.store.AuthenticateUser(r.Context(), username, password)
	if errors.Is(err, store.ErrNotFound) {
		s.logins.fail(r.Context(), rlKey)
		if accountLimited {
			s.logins.accountFail(r.Context(), username)
		}
		s.auditLogin(r, username, "login_failed", "falsche Zugangsdaten")
		s.setFlash(w, r, "error", "Benutzername oder Passwort falsch.")
		redirect(w, r, "/login")
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.logins.reset(r.Context(), rlKey)
	if accountLimited {
		s.logins.accountReset(r.Context(), username)
	}

	if user.TotpEnabled {
		// Mitigation: Check per-user rate limit before showing the 2FA step.
		// This prevents users who are already locked out from even seeing the
		// 2FA form, and protects against 2FA brute-forcing.
		if s.sensitiveBlocked(w, r, user.ID, "/login") {
			return
		}
		s.setCookie(w, r, &http.Cookie{
			Name:     pending2FACookie,
			Value:    s.signPending2FA(user.ID),
			MaxAge:   int(pending2FATTL.Seconds()),
			SameSite: http.SameSiteStrictMode, // Hardened to Strict for short-lived login flow
		})
		s.setFlash(w, r, "info", "Bitte den 6‑stelligen Code deiner Authenticator‑App eingeben.")
		redirect(w, r, "/login")
		return
	}
	s.establishSession(w, r, user)
}

// handleLogin2FA is step 2: verify the TOTP code for the pending user.
func (s *Server) handleLogin2FA(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	c, err := r.Cookie(pending2FACookie)
	if err != nil {
		s.setFlash(w, r, "error", "Anmeldung abgelaufen. Bitte erneut anmelden.")
		redirect(w, r, "/login")
		return
	}
	userID, ok := s.verifyPending2FA(c.Value)
	if !ok {
		s.clearPending2FA(w, r)
		s.setFlash(w, r, "error", "Anmeldung abgelaufen. Bitte erneut anmelden.")
		redirect(w, r, "/login")
		return
	}
	// Mitigation: Enforce per-user rate limiting on the 2FA step to protect
	// against distributed brute-force attacks on the 6-digit TOTP code.
	if s.sensitiveBlocked(w, r, userID, "/login") {
		return
	}
	rlKey := s.clientIP(r)
	if s.logins.blocked(r.Context(), rlKey) {
		s.setFlash(w, r, "error", "Zu viele Fehlversuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, "/login")
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil {
		s.clearPending2FA(w, r)
		redirect(w, r, "/login")
		return
	}
	input := r.FormValue("totp")
	secret, _ := s.store.GetTotpSecret(r.Context(), userID)

	// Validate the TOTP code, then enforce replay protection: a matched code is
	// only accepted if its time-step hasn't been consumed before (atomic
	// compare-and-set), so an observed/echoed code cannot be reused within its
	// ~30-90s window.
	totpOK := false
	if step, ok := totp.ValidateStep(secret, input); ok {
		if accepted, err := s.store.AcceptTotpStep(r.Context(), userID, step); err == nil && accepted {
			totpOK = true
		}
	}

	switch {
	case totpOK:
		// authenticator code accepted
	case auth.LooksLikeRecoveryCode(input) && s.consumeRecovery(r, userID, input):
		// one-time recovery code accepted
		remaining, _ := s.store.CountUnusedRecoveryCodes(r.Context(), userID)
		s.auditLogin(r, user.Username, "login_recovery", itoa(remaining)+" Codes übrig")
		s.setFlash(w, r, "info", "Mit Wiederherstellungscode angemeldet. Noch "+itoa(remaining)+" Code(s) übrig.")
	default:
		s.logins.fail(r.Context(), rlKey)
		// Mitigation: Record a failure in the per-user limiter to prevent
		// brute-forcing across multiple IP addresses.
		s.sensitiveFail(r, userID)
		s.auditLogin(r, user.Username, "login_2fa_failed", "")
		s.setFlash(w, r, "error", "Code ungültig. Bitte erneut versuchen.")
		redirect(w, r, "/login") // pending cookie stays -> 2FA step shown again
		return
	}
	s.logins.reset(r.Context(), rlKey)
	s.sensitiveReset(r, userID)
	s.clearPending2FA(w, r)
	s.establishSession(w, r, user)
}

// consumeRecovery reports whether the input matches (and consumes) an unused
// recovery code for the user.
func (s *Server) consumeRecovery(r *http.Request, userID int64, input string) bool {
	ok, err := s.store.ConsumeRecoveryCode(r.Context(), userID, auth.HashRecoveryCode(input))
	return err == nil && ok
}

// establishSession creates the login session cookie and finishes the login.
func (s *Server) establishSession(w http.ResponseWriter, r *http.Request, user *models.User) {
	if !s.startSession(w, r, user) {
		return
	}
	redirect(w, r, "/")
}

// startSession creates the session, sets the cookie and audits the login,
// without writing a response body. Returns false (after emitting a 500) on
// failure. Used directly by API-style logins (e.g. passkeys) that return JSON.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *models.User) bool {
	token, err := s.store.CreateSession(r.Context(), user.ID, sessionTTL, r.UserAgent(), s.clientIP(r))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return false
	}
	s.setCookie(w, r, &http.Cookie{
		Name:   s.sessionCookieName(r),
		Value:  token,
		MaxAge: int(sessionTTL.Seconds()),
	})
	_ = s.store.AddAudit(r.Context(), &user.ID, user.Username, "login", "auth", "", "", s.clientIP(r)) // best-effort audit line
	return true
}

// ---- Signed pending-2FA token (survives step 1 -> step 2, no DB state) ----

func (s *Server) signPending2FA(userID int64) string {
	// The "2fa:" prefix binds the HMAC to this context, so a pending-2FA token
	// can never be replayed as another value signed with the same secret
	// (mirrors the "csrf:" prefix in csrf.go).
	payload := fmt.Sprintf("2fa:%d|%d", userID, time.Now().Add(pending2FATTL).Unix())
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

// maxPending2FATokenLen bounds the raw pending-2FA cookie input so an oversized
// payload cannot drive unnecessary string or base64 decoding allocations (DoS defense).
const maxPending2FATokenLen = 200

func (s *Server) verifyPending2FA(value string) (int64, bool) {
	if len(value) > maxPending2FATokenLen {
		return 0, false
	}
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return 0, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write(raw)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[1])) {
		return 0, false
	}
	var uid, exp int64
	if _, err := fmt.Sscanf(string(raw), "2fa:%d|%d", &uid, &exp); err != nil {
		return 0, false
	}
	if time.Now().Unix() > exp {
		return 0, false
	}
	return uid, true
}

func (s *Server) clearPending2FA(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, &http.Cookie{Name: pending2FACookie, Value: "", MaxAge: -1})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if u := s.currentUser(r); u != nil {
		_ = s.store.AddAudit(r.Context(), &u.ID, u.Username, "logout", "auth", "", "", s.clientIP(r)) // best-effort audit line
	}
	if c, err := r.Cookie(s.sessionCookieName(r)); err == nil && c.Value != "" {
		// Invalidate the server-side session, not just the cookie: if this fails the
		// token stays valid server-side, so a captured token would still authenticate.
		if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
			slog.Error("logout: delete session failed", "err", sanitizeLog(err.Error()))
		}
	}
	s.setCookie(w, r, &http.Cookie{Name: s.sessionCookieName(r), Value: "", MaxAge: -1})
	redirect(w, r, "/login")
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	sessions, err := s.store.ListSessionsForUser(r.Context(), user.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Sessions carry the stored token *hash*; hash the current cookie to match.
	currentHash := ""
	if c, err := r.Cookie(s.sessionCookieName(r)); err == nil {
		currentHash = store.HashToken(c.Value)
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].Token == currentHash
	}
	data := s.newPage(w, r, "Einstellungen", "profile")
	data["Sessions"] = sessions
	// Passkeys are managed inline on this page.
	creds, err := s.store.ListWebauthnCredentials(r.Context(), user.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Passkeys"] = creds
	// Remaining recovery codes drive the 2FA card's count chip (only when 2FA
	// is enabled; the setup flow lives on its own focused page).
	if user.TotpEnabled {
		remaining, err := s.store.CountUnusedRecoveryCodes(r.Context(), user.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		data["RecoveryRemaining"] = remaining
	}
	s.render(w, r, "profile", data)
}
