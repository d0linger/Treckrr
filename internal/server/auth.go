package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"treckrr/internal/auth"
	"treckrr/internal/models"
	"treckrr/internal/store"
	"treckrr/internal/totp"
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
	data := pageData{"Title": "Anmelden", "Theme": themeFromCookie(r), "CSRF": s.csrfToken(r)}
	// If a valid pending-2FA cookie is present, show the second step instead.
	if c, err := r.Cookie(pending2FACookie); err == nil {
		if _, ok := s.verifyPending2FA(c.Value); ok {
			data["ShowTotp"] = true
		}
	}
	if msg, kind := s.readFlash(w, r); msg != "" {
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	rlKey := s.clientIP(r)

	if s.logins.blocked(r.Context(), rlKey) {
		s.auditLogin(r, username, "login_blocked", "zu viele Fehlversuche")
		s.setFlash(w, r, "error", "Zu viele Fehlversuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, "/login")
		return
	}

	user, err := s.store.AuthenticateUser(r.Context(), username, password)
	if errors.Is(err, store.ErrNotFound) {
		s.logins.fail(r.Context(), rlKey)
		s.auditLogin(r, username, "login_failed", "falsche Zugangsdaten")
		s.setFlash(w, r, "error", "Benutzername oder Passwort falsch.")
		redirect(w, r, "/login")
		return
	}
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.logins.reset(r.Context(), rlKey)

	if user.TotpEnabled {
		s.setCookie(w, r, &http.Cookie{
			Name:     pending2FACookie,
			Value:    s.signPending2FA(user.ID),
			HttpOnly: true,
			MaxAge:   int(pending2FATTL.Seconds()),
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
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

	switch {
	case totp.Validate(secret, input):
		// authenticator code accepted
	case auth.LooksLikeRecoveryCode(input) && s.consumeRecovery(r, userID, input):
		// one-time recovery code accepted
		remaining, _ := s.store.CountUnusedRecoveryCodes(r.Context(), userID)
		s.auditLogin(r, user.Username, "login_recovery", itoa(remaining)+" Codes übrig")
		s.setFlash(w, r, "info", "Mit Wiederherstellungscode angemeldet. Noch "+itoa(remaining)+" Code(s) übrig.")
	default:
		s.logins.fail(r.Context(), rlKey)
		s.auditLogin(r, user.Username, "login_2fa_failed", "")
		s.setFlash(w, r, "error", "Code ungültig. Bitte erneut versuchen.")
		redirect(w, r, "/login") // pending cookie stays -> 2FA step shown again
		return
	}
	s.logins.reset(r.Context(), rlKey)
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
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return false
	}
	s.setCookie(w, r, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		HttpOnly: true,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	_ = s.store.AddAudit(r.Context(), &user.ID, user.Username, "login", "auth", "", "", s.clientIP(r))
	return true
}

// ---- Signed pending-2FA token (survives step 1 -> step 2, no DB state) ----

func (s *Server) signPending2FA(userID int64) string {
	payload := fmt.Sprintf("%d|%d", userID, time.Now().Add(pending2FATTL).Unix())
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + hex.EncodeToString(mac.Sum(nil))
}

func (s *Server) verifyPending2FA(value string) (int64, bool) {
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
	if _, err := fmt.Sscanf(string(raw), "%d|%d", &uid, &exp); err != nil {
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
		_ = s.store.AddAudit(r.Context(), &u.ID, u.Username, "logout", "auth", "", "", s.clientIP(r))
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		_ = s.store.DeleteSession(r.Context(), c.Value)
	}
	s.setCookie(w, r, &http.Cookie{Name: sessionCookie, Value: "", MaxAge: -1})
	redirect(w, r, "/login")
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	sessions, err := s.store.ListSessionsForUser(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	currentToken := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		currentToken = c.Value
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].Token == currentToken
	}
	data := s.newPage(w, r, "Profil", "profile")
	data["Sessions"] = sessions
	s.render(w, r, "profile", data)
}
