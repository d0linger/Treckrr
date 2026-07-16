package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

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

func (s *Server) saveWASession(w http.ResponseWriter, r *http.Request, sd *webauthn.SessionData) {
	b, _ := json.Marshal(sd)
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write(b)
	val := base64.RawURLEncoding.EncodeToString(b) + "." + hex.EncodeToString(mac.Sum(nil))
	s.setCookie(w, r, &http.Cookie{Name: waCookie, Value: val, MaxAge: 300})
}

func (s *Server) loadWASession(r *http.Request) (*webauthn.SessionData, bool) {
	c, err := r.Cookie(waCookie)
	if err != nil {
		return nil, false
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write(b)
	if !hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[1])) {
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
	wu, err := s.webauthnUserFor(r, user)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	creation, sd, err := s.wa.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(wu.creds).CredentialDescriptors()),
	)
	if err != nil {
		log.Printf("passkey register begin failed: user=%s reason=%s",
			sanitizeLog(user.Username), sanitizeLog(webauthnErrReason(err)))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.saveWASession(w, r, sd)
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
		log.Printf("passkey register finish failed: user=%s ua=%q reason=%s",
			sanitizeLog(user.Username), sanitizeLog(r.UserAgent()), sanitizeLog(reason))
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
	assertion, sd, err := s.wa.BeginDiscoverableLogin()
	if err != nil {
		log.Printf("passkey login begin failed: ip=%s reason=%s",
			sanitizeLog(s.clientIP(r)), sanitizeLog(webauthnErrReason(err)))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.saveWASession(w, r, sd)
	writeJSON(w, assertion)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	sd, ok := s.loadWASession(r)
	if !ok {
		// The begin→finish challenge cookie is missing or failed HMAC/decoding.
		// Common behind a misconfigured proxy (cookie dropped, or Secure/SameSite
		// mismatch), so record it instead of returning silently.
		log.Printf("passkey login: challenge cookie missing/invalid ip=%s", sanitizeLog(s.clientIP(r)))
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
		log.Printf("passkey login failed: ip=%s ua=%q reason=%s",
			sanitizeLog(s.clientIP(r)), sanitizeLog(r.UserAgent()), sanitizeLog(reason))
		s.auditLogin(r, "", "login_passkey_failed", reason)
		http.Error(w, "Anmeldung mit Passkey fehlgeschlagen.", http.StatusUnauthorized)
		return
	}
	s.logins.reset(r.Context(), rlKey)
	// Clone detection: go-webauthn flags a regressed signature counter (never
	// for counter-less synced authenticators, which stay at 0). Surface it —
	// login still proceeds, but an admin can see the signal in the trail.
	if cred.Authenticator.CloneWarning {
		log.Printf("passkey login: possible clone (signature counter regressed) user=%s ip=%s",
			sanitizeLog(loggedIn.Username), sanitizeLog(s.clientIP(r)))
		s.auditLogin(r, loggedIn.Username, "login_passkey_clone_warning", "Signaturzähler rückläufig – möglicher Klon")
	}
	// Persist the updated counter/backup-state; a failure here would leave stale
	// state for the next assertion, so log it rather than swallowing it.
	if err := s.store.TouchWebauthnCredential(r.Context(), cred.ID, cred.Authenticator.SignCount, cred.Flags.BackupState); err != nil {
		log.Printf("passkey login: credential state update failed user=%s: %v",
			sanitizeLog(loggedIn.Username), sanitizeLog(err.Error()))
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
