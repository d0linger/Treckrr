package server

import (
	"context"
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
	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/totp"
)

const (
	pending2FACookie = "treckrr_2fa"
	pending2FATTL    = 5 * time.Minute
)

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	// auth()/admin() mark every authenticated page no-store, but the login page is
	// public and so was getting no Cache-Control at all — while carrying a CSRF
	// token, a Set-Cookie and, at the second step, the fact that a valid password
	// was just accepted. Nothing here may sit in a shared or on-disk cache.
	w.Header().Set("Cache-Control", "no-store")
	if s.currentUser(r) != nil {
		redirect(w, r, "/")
		return
	}
	// A session cookie that no longer resolves is dead weight at best; drop it
	// so the browser stops presenting it.
	s.expireStaleSession(w, r)
	// "Abbrechen" from the 2FA step clears the pending state.
	if r.URL.Query().Get("cancel") == "1" {
		s.clearPending2FA(w, r)
		redirect(w, r, "/login")
		return
	}
	// Both forms on this page embed their own purpose-bound token, so the
	// generic session-token injection is switched off here.
	data := pageData{
		"Title": "Anmelden", "Theme": s.themeFromCookie(r), "CSRF": s.loginCSRFToken(w, r),
		skipCSRFInjectKey: true,
	}
	// If a valid pending-2FA cookie is present, show the second step instead.
	if c, err := s.cookie(r, pending2FACookie); err == nil && c.Value != "" {
		if _, ok := s.verifyPending2FA(r.Context(), c.Value); ok {
			data["ShowTotp"] = true
			data["TwoFactorCSRF"] = s.pending2FACSRFToken(c.Value)
		} else {
			// Expired, forged or invalidated by a credential change: drop it so
			// the password step starts clean.
			s.clearPending2FA(w, r)
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
	rlKey := s.limiterIP(r)

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
	allowed, failed := s.admitVerification(w, r, rlKey, loginMaxFails, loginWindow)
	if failed {
		return
	}
	if allowed && accountLimited {
		allowed, failed = s.admitVerification(w, r, accountKey(username), accountMaxFails, accountWindow)
		if failed {
			return
		}
	}
	if !allowed {
		metrics.Inc(metrics.LoginBlocked)
		s.auditLogin(r, username, "login_blocked", "zu viele Fehlversuche")
		s.setFlash(w, r, "error", "Zu viele Fehlversuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, "/login")
		return
	}

	user, err := s.store.AuthenticateUser(r.Context(), username, password)
	if errors.Is(err, store.ErrNotFound) {
		metrics.Inc(metrics.LoginFailed)
		s.auditLogin(r, username, "login_failed", "falsche Zugangsdaten")
		s.setFlash(w, r, "error", "Benutzername oder Passwort falsch.")
		redirect(w, r, "/login")
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Give back only this attempt's reservation. The per-IP bucket is shared by
	// every account behind that address, so wiping it on success let anyone with
	// a working login spray guesses at other accounts without limit: four misses,
	// one login to their own account, repeat.
	s.logins.refund(r.Context(), rlKey)
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
		binding, err := s.store.PendingTwoFactorBinding(r.Context(), user.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		s.setCookie(w, r, &http.Cookie{
			Name:     pending2FACookie,
			Value:    s.signPending2FA(user.ID, binding),
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
	c, err := s.cookie(r, pending2FACookie)
	if err != nil || c.Value == "" {
		s.setFlash(w, r, "error", "Anmeldung abgelaufen. Bitte erneut anmelden.")
		redirect(w, r, "/login")
		return
	}
	// This path is exempt from the generic csrf middleware (preSessionAuthPaths);
	// its token is bound to the pending-2FA cookie instead of a session.
	if !s.verifyPending2FACSRF(r, c.Value) {
		metrics.Inc(metrics.CSRFRejected)
		s.setFlash(w, r, "error", "Sicherheits-Token abgelaufen. Bitte erneut versuchen.")
		redirect(w, r, "/login")
		return
	}
	input := r.FormValue("totp")
	if s.tooLong(
		w, r, "Code", input, maxNameLen,
	) {
		redirect(w, r, "/login")
		return
	}
	userID, ok := s.verifyPending2FA(r.Context(), c.Value)
	if !ok {
		s.clearPending2FA(w, r)
		s.setFlash(w, r, "error", "Anmeldung abgelaufen. Bitte erneut anmelden.")
		redirect(w, r, "/login")
		return
	}
	// Mitigation: Enforce per-user rate limiting on the 2FA step to protect
	// against distributed brute-force attacks on the 6-digit TOTP code.
	if !s.sensitiveAdmit(w, r, userID, "/login") {
		return
	}
	rlKey := s.limiterIP(r)
	allowed, failed := s.admitVerification(w, r, rlKey, loginMaxFails, loginWindow)
	if failed {
		return
	}
	if !allowed {
		s.setFlash(w, r, "error", "Zu viele Fehlversuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, "/login")
		return
	}
	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil || user.Disabled || !user.TotpEnabled {
		s.clearPending2FA(w, r)
		redirect(w, r, "/login")
		return
	}
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
		s.auditLogin(r, user.Username, "login_2fa_failed", "")
		s.setFlash(w, r, "error", "Code ungültig. Bitte erneut versuchen.")
		redirect(w, r, "/login") // pending cookie stays -> 2FA step shown again
		return
	}
	s.logins.refund(r.Context(), rlKey) // this attempt only; see handleLogin
	s.sensitiveReset(r, userID)
	s.establishSession(w, r, user)
}

// sessionGone reports whether a session lookup failed because the session no
// longer exists, as opposed to a transient store error.
func sessionGone(err error) bool {
	return errors.Is(err, store.ErrNotFound)
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
	s.clearTransitionalAuthCookies(w, r)
	s.setCookie(w, r, &http.Cookie{
		Name:   sessionCookie,
		Value:  token,
		MaxAge: int(sessionTTL.Seconds()),
	})
	return true
}

// ---- Signed pending-2FA token (survives step 1 -> step 2, no DB state) ----

// signPending2FA issues the pending-2FA token for userID. binding is the
// digest of the account's credential state at issue time
// (store.PendingTwoFactorBinding); it enters the MAC but not the visible
// payload. Any later change to that state — a completed second step (which
// advances totp_last_step or consumes a recovery code), a password change, an
// admin reset — therefore invalidates every outstanding token, so a copied
// cookie can no longer skip the password step repeatedly for its whole TTL.
func (s *Server) signPending2FA(userID int64, binding []byte) string {
	payload := fmt.Sprintf("2fa:%d|%d", userID, time.Now().Add(pending2FATTL).Unix())
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		hex.EncodeToString(s.pending2FAMAC(payload, binding))
}

// pending2FAMAC signs a pending-2FA payload together with its binding. The
// "2fa:" payload prefix binds the HMAC to this context, so the token can never
// be replayed as another value signed with the same secret (mirrors the "csrf:"
// prefix in csrf.go).
func (s *Server) pending2FAMAC(payload string, binding []byte) []byte {
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(payload))
	mac.Write([]byte{0})
	mac.Write(binding)
	return mac.Sum(nil)
}

// maxPending2FATokenLen bounds the raw pending-2FA cookie input so an oversized
// payload cannot drive unnecessary string or base64 decoding allocations (DoS defense).
const maxPending2FATokenLen = 200

// verifyPending2FA checks a pending-2FA token against the account's current
// credential state and returns the user it was issued for.
func (s *Server) verifyPending2FA(ctx context.Context, value string) (int64, bool) {
	return s.verifyPending2FAWith(value, func(userID int64) ([]byte, error) {
		return s.store.PendingTwoFactorBinding(ctx, userID)
	})
}

// verifyPending2FAWith is verifyPending2FA with the binding lookup injected. The
// cheap structural and expiry checks run before the lookup.
func (s *Server) verifyPending2FAWith(value string, binding func(int64) ([]byte, error)) (int64, bool) {
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
	var uid, exp int64
	if _, err := fmt.Sscanf(string(raw), "2fa:%d|%d", &uid, &exp); err != nil {
		return 0, false
	}
	if time.Now().Unix() > exp {
		return 0, false
	}
	b, err := binding(uid)
	if err != nil {
		return 0, false
	}
	if !hmac.Equal([]byte(hex.EncodeToString(s.pending2FAMAC(string(raw), b))), []byte(parts[1])) {
		return 0, false
	}
	return uid, true
}

// clearPending2FA expires the signed cookie that authorizes the second login step.
func (s *Server) clearPending2FA(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, &http.Cookie{Name: pending2FACookie, Value: "", MaxAge: -1})
}

// clearTransitionalAuthCookies removes pre-session state once authentication
// succeeds or the user logs out, so it cannot outlive the login flow.
func (s *Server) clearTransitionalAuthCookies(w http.ResponseWriter, r *http.Request) {
	s.clearPending2FA(w, r)
	s.setCookie(w, r, &http.Cookie{Name: loginCSRFCookie, Value: "", MaxAge: -1})
}

// handleLogout invalidates the server-side session and records success
// transactionally before expiring browser state. A database failure preserves
// the session cookie so the user can retry revocation.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Once browser state is expired, the browser also drops its HTTP cache for
	// this origin. Deliberately only "cache", never "storage": storage would also
	// wipe the origin-wide offline queue in IndexedDB, including other users'
	// unsent bookings on a shared device, and the service worker. The queue is
	// handled client-side before this POST instead (offline.js offers to keep, or
	// to export and delete, the current user's items; quarantined items expire).
	clearSiteData := func() { w.Header().Set("Clear-Site-Data", `"cache"`) }
	c, err := s.cookie(r, sessionCookie)
	if err != nil || c.Value == "" {
		s.clearTransitionalAuthCookies(w, r)
		s.setCookie(w, r, &http.Cookie{Name: sessionCookie, Value: "", MaxAge: -1})
		clearSiteData()
		redirect(w, r, "/login")
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_, err = s.store.LogoutSession(ctx, c.Value, s.clientIP(r))
	if err != nil {
		slog.Error("logout failed", "err", sanitizeLog(err.Error()))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Retry-After", "5")
		http.Error(w, "Abmeldung fehlgeschlagen. Bitte erneut versuchen.", http.StatusServiceUnavailable)
		return
	}
	// A missing row means the server-side bearer is already invalid; clearing the
	// browser state is safe even though there is no new success event to audit.
	s.clearTransitionalAuthCookies(w, r)
	s.setCookie(w, r, &http.Cookie{Name: sessionCookie, Value: "", MaxAge: -1})
	clearSiteData()
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
	if c, err := s.cookie(r, sessionCookie); err == nil {
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
