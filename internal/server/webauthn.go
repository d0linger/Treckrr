package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"treckrr/internal/models"
)

// webauthnErrReason extracts a concise, log-safe reason from a WebAuthn error.
// go-webauthn returns *protocol.Error with a type, a short detail and (most
// useful for diagnosis) DevInfo — e.g. "Error validating origin". Plain errors
// fall back to their message.
func webauthnErrReason(err error) string {
	if err == nil {
		return ""
	}
	var pe *protocol.Error
	if errors.As(err, &pe) {
		parts := make([]string, 0, 3)
		for _, p := range []string{pe.Type, pe.Details, pe.DevInfo} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ": ")
		}
	}
	return err.Error()
}

// waCookie holds the short-lived, HMAC-signed WebAuthn challenge/session between
// the begin and finish steps of a ceremony (opaque to the client).
const waCookie = "treckrr_wa"

// webauthnUser adapts a Treckrr user + its credentials to the webauthn.User
// interface. The handle (not the DB id) is the stable authenticator identifier.
type webauthnUser struct {
	name   string
	handle []byte
	creds  []webauthn.Credential
}

func (u *webauthnUser) WebAuthnID() []byte                         { return u.handle }
func (u *webauthnUser) WebAuthnName() string                       { return u.name }
func (u *webauthnUser) WebAuthnDisplayName() string                { return u.name }
func (u *webauthnUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func (s *Server) webauthnUserFor(r *http.Request, u *models.User) (*webauthnUser, error) {
	handle, err := s.store.WebauthnHandle(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	creds, err := s.store.ListWebauthnCredentials(r.Context(), u.ID)
	if err != nil {
		return nil, err
	}
	return &webauthnUser{name: u.Username, handle: handle, creds: toWACreds(creds)}, nil
}

func toWACreds(list []models.WebauthnCredential) []webauthn.Credential {
	out := make([]webauthn.Credential, 0, len(list))
	for _, c := range list {
		var transports []protocol.AuthenticatorTransport
		for _, t := range strings.Split(c.Transports, ",") {
			if t != "" {
				transports = append(transports, protocol.AuthenticatorTransport(t))
			}
		}
		wc := webauthn.Credential{
			ID:            c.CredentialID,
			PublicKey:     c.PublicKey,
			Transport:     transports,
			Authenticator: webauthn.Authenticator{AAGUID: c.AAGUID, SignCount: c.SignCount},
		}
		// Replay the BE/BS flags observed at registration; go-webauthn requires
		// the stored BackupEligible flag to match the assertion on every login.
		wc.Flags.BackupEligible = c.BackupEligible
		wc.Flags.BackupState = c.BackupState
		out = append(out, wc)
	}
	return out
}

func fromWACred(c *webauthn.Credential, name string) models.WebauthnCredential {
	ts := make([]string, 0, len(c.Transport))
	for _, t := range c.Transport {
		ts = append(ts, string(t))
	}
	return models.WebauthnCredential{
		CredentialID:   c.ID,
		PublicKey:      c.PublicKey,
		AAGUID:         c.Authenticator.AAGUID,
		SignCount:      c.Authenticator.SignCount,
		Transports:     strings.Join(ts, ","),
		Name:           name,
		BackupEligible: c.Flags.BackupEligible,
		BackupState:    c.Flags.BackupState,
	}
}

// ---- signed challenge cookie --------------------------------------------

// waCeremonyTTL bounds how long a begin→finish ceremony stays valid server-side.
const waCeremonyTTL = 5 * time.Minute

// saveWASession stores the ceremony session server-side under a random id (SH-03)
// and puts only that id — HMAC-signed against tampering — in the short-lived
// cookie. The server-side row carries the expiry and is single-use on finish.
func (s *Server) saveWASession(w http.ResponseWriter, r *http.Request, sd *webauthn.SessionData) error {
	b, err := json.Marshal(sd)
	if err != nil {
		return err
	}
	idBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	if err := s.store.CreateWebauthnCeremony(r.Context(), id, b, time.Now().Add(waCeremonyTTL)); err != nil {
		return err
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("wa:" + id))
	val := id + "." + hex.EncodeToString(mac.Sum(nil))
	s.setCookie(w, r, &http.Cookie{
		Name:     waCookie,
		Value:    val,
		MaxAge:   int(waCeremonyTTL.Seconds()),
		SameSite: http.SameSiteStrictMode, // short-lived login flow
	})
	return nil
}

// loadWASession verifies the cookie's signed id and atomically consumes the
// server-side ceremony (single-use, server-expiring). A replayed or expired
// ceremony returns false.
func (s *Server) loadWASession(r *http.Request) (*webauthn.SessionData, bool) {
	c, err := r.Cookie(waCookie)
	if err != nil {
		return nil, false
	}
	id, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return nil, false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("wa:" + id))
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(sig)) {
		return nil, false
	}
	b, err := s.store.ConsumeWebauthnCeremony(r.Context(), id)
	if err != nil {
		return nil, false
	}
	var sd webauthn.SessionData
	if err := json.Unmarshal(b, &sd); err != nil {
		return nil, false
	}
	return &sd, true
}

func (s *Server) clearWASession(w http.ResponseWriter, r *http.Request) {
	s.setCookie(w, r, &http.Cookie{Name: waCookie, Value: "", MaxAge: -1})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- passkey management page --------------------------------------------

// handlePasskeys previously rendered a standalone page; passkey management now
// lives inline on the Einstellungen overview, so this route just redirects.
func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request) {
	redirect(w, r, "/profile")
}

func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.DeleteWebauthnCredential(r.Context(), user.ID, id); err != nil {
		s.setFlash(w, r, "error", "Passkey konnte nicht entfernt werden.")
	} else {
		s.audit(r, "passkey_delete", "user", user.ID, "")
		s.setFlash(w, r, "success", "Passkey entfernt.")
	}
	redirect(w, r, "/profile")
}

// ---- registration ceremony (authenticated) ------------------------------

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	// Step-up: adding a durable passkey requires re-entering the password, so a
	// hijacked session can't silently enroll one (SH-02). The body is bounded by
	// limitBody.
	var body struct {
		Password string `json:"password"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, body.Password); err != nil {
		s.audit(r, "passkey_add_denied", "user", user.ID, "Passwort falsch")
		http.Error(w, "Passwort falsch.", http.StatusForbidden)
		return
	}
	wu, err := s.webauthnUserFor(r, user)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	creation, sd, err := s.wa.BeginRegistration(wu,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired, // T-03: PIN/biometric, not presence-only
		}),
		webauthn.WithExclusions(webauthn.Credentials(wu.creds).CredentialDescriptors()),
	)
	if err != nil {
		slog.Warn("passkey register begin failed",
			"user", sanitizeLog(user.Username), "reason", sanitizeLog(webauthnErrReason(err)))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if err := s.saveWASession(w, r, sd); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	writeJSON(w, creation)
}

func (s *Server) handlePasskeyRegisterFinish(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	sd, ok := s.loadWASession(r)
	if !ok {
		http.Error(w, "Challenge abgelaufen. Bitte erneut versuchen.", http.StatusBadRequest)
		return
	}
	s.clearWASession(w, r)
	wu, err := s.webauthnUserFor(r, user)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	cred, err := s.wa.FinishRegistration(wu, *sd, r)
	if err != nil {
		reason := webauthnErrReason(err)
		slog.Warn("passkey register finish failed",
			"user", sanitizeLog(user.Username), "ua", sanitizeLog(r.UserAgent()), "reason", sanitizeLog(reason))
		s.audit(r, "passkey_add_failed", "user", user.ID, reason)
		http.Error(w, "Passkey-Registrierung fehlgeschlagen.", http.StatusBadRequest)
		return
	}
	name := deviceName(r.UserAgent())
	if err := s.store.AddWebauthnCredential(r.Context(), user.ID, fromWACred(cred, name)); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "passkey_add", "user", user.ID, name)
	writeJSON(w, map[string]string{"status": "ok"})
}

// ---- login ceremony (discoverable / usernameless, public) ---------------

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	// Rate-limit begin too: it now persists a server-side ceremony row (SH-03), so a
	// blocked IP must not be able to spam ceremony creation.
	if s.logins.blocked(r.Context(), s.clientIP(r)) {
		s.auditLogin(r, "", "login_passkey_failed", "Rate-Limit: zu viele Fehlversuche")
		http.Error(w, "Zu viele Fehlversuche. Bitte später erneut versuchen.", http.StatusTooManyRequests)
		return
	}
	assertion, sd, err := s.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired), // T-03
	)
	if err != nil {
		slog.Warn("passkey login begin failed",
			"ip", sanitizeLog(s.clientIP(r)), "reason", sanitizeLog(webauthnErrReason(err)))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if err := s.saveWASession(w, r, sd); err != nil {
		slog.Error("passkey login begin: save ceremony failed", "ip", sanitizeLog(s.clientIP(r)), "err", sanitizeLog(err.Error()))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	writeJSON(w, assertion)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	sd, ok := s.loadWASession(r)
	if !ok {
		// The begin→finish challenge cookie is missing or failed HMAC/decoding.
		// Common behind a misconfigured proxy (cookie dropped, or Secure/SameSite
		// mismatch), so record it instead of returning silently.
		slog.Warn("passkey login: challenge cookie missing/invalid", "ip", sanitizeLog(s.clientIP(r)))
		s.auditLogin(r, "", "login_passkey_failed", "Challenge fehlt oder abgelaufen (Cookie nicht empfangen)")
		http.Error(w, "Challenge abgelaufen. Bitte erneut versuchen.", http.StatusBadRequest)
		return
	}
	s.clearWASession(w, r)

	rlKey := s.clientIP(r)
	if s.logins.blocked(r.Context(), rlKey) {
		s.auditLogin(r, "", "login_passkey_failed", "Rate-Limit: zu viele Fehlversuche")
		http.Error(w, "Zu viele Fehlversuche. Bitte später erneut versuchen.", http.StatusTooManyRequests)
		return
	}

	var loggedIn *models.User
	var handlerErr error
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.store.UserByWebauthnHandle(r.Context(), userHandle)
		if err != nil {
			handlerErr = err
			return nil, err
		}
		wu, err := s.webauthnUserFor(r, u)
		if err != nil {
			handlerErr = err
			return nil, err
		}
		loggedIn = u
		return wu, nil
	}
	cred, err := s.wa.FinishDiscoverableLogin(handler, *sd, r)
	if err != nil || loggedIn == nil {
		s.logins.fail(r.Context(), rlKey)
		reason := webauthnErrReason(err)
		if reason == "" && handlerErr != nil {
			reason = "Benutzer/Passkey nicht gefunden: " + handlerErr.Error()
		}
		if reason == "" {
			reason = "kein passender Passkey gefunden"
		}
		slog.Warn("passkey login failed",
			"ip", sanitizeLog(s.clientIP(r)), "ua", sanitizeLog(r.UserAgent()), "reason", sanitizeLog(reason))
		s.auditLogin(r, "", "login_passkey_failed", reason)
		http.Error(w, "Anmeldung mit Passkey fehlgeschlagen.", http.StatusUnauthorized)
		return
	}
	s.logins.reset(r.Context(), rlKey)
	// Clone detection: go-webauthn flags a regressed signature counter (never
	// for counter-less synced authenticators, which stay at 0). Surface it —
	// login still proceeds, but an admin can see the signal in the trail.
	if cred.Authenticator.CloneWarning {
		slog.Warn("passkey login: possible clone (signature counter regressed)",
			"user", sanitizeLog(loggedIn.Username), "ip", sanitizeLog(s.clientIP(r)))
		s.auditLogin(r, loggedIn.Username, "login_passkey_clone_warning", "Signaturzähler rückläufig – möglicher Klon")
	}
	// Persist the updated counter/backup-state; a failure here would leave stale
	// state for the next assertion, so log it rather than swallowing it.
	if err := s.store.TouchWebauthnCredential(r.Context(), cred.ID, cred.Authenticator.SignCount, cred.Flags.BackupState); err != nil {
		slog.Error("passkey login: credential state update failed",
			"user", sanitizeLog(loggedIn.Username), "err", sanitizeLog(err.Error()))
	}
	s.auditLogin(r, loggedIn.Username, "login_passkey", "")
	if !s.startSession(w, r, loggedIn) {
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "redirect": "/"})
}

// deviceName derives a friendly passkey label from the user agent.
func deviceName(ua string) string {
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		return "Apple-Gerät"
	case strings.Contains(ua, "Android"):
		return "Android-Gerät"
	case strings.Contains(ua, "Mac"):
		return "Mac"
	case strings.Contains(ua, "Windows"):
		return "Windows"
	default:
		return "Passkey"
	}
}
