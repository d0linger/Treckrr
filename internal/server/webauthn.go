package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"treckrr/internal/models"
)

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
		out = append(out, webauthn.Credential{
			ID:            c.CredentialID,
			PublicKey:     c.PublicKey,
			Transport:     transports,
			Authenticator: webauthn.Authenticator{AAGUID: c.AAGUID, SignCount: c.SignCount},
		})
	}
	return out
}

func fromWACred(c *webauthn.Credential, name string) models.WebauthnCredential {
	ts := make([]string, 0, len(c.Transport))
	for _, t := range c.Transport {
		ts = append(ts, string(t))
	}
	return models.WebauthnCredential{
		CredentialID: c.ID,
		PublicKey:    c.PublicKey,
		AAGUID:       c.Authenticator.AAGUID,
		SignCount:    c.Authenticator.SignCount,
		Transports:   strings.Join(ts, ","),
		Name:         name,
	}
}

// ---- signed challenge cookie --------------------------------------------

func (s *Server) saveWASession(w http.ResponseWriter, r *http.Request, sd *webauthn.SessionData) {
	b, _ := json.Marshal(sd)
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write(b)
	val := base64.RawURLEncoding.EncodeToString(b) + "." + hex.EncodeToString(mac.Sum(nil))
	s.setCookie(w, r, &http.Cookie{Name: waCookie, Value: val, HttpOnly: true, MaxAge: 300})
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

func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	creds, err := s.store.ListWebauthnCredentials(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	data := s.newPage(w, r, "Passkeys", "profile")
	data["Passkeys"] = creds
	s.render(w, r, "passkeys", data)
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
	redirect(w, r, "/account/passkeys")
}

// ---- registration ceremony (authenticated) ------------------------------

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	wu, err := s.webauthnUserFor(r, user)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	creation, sd, err := s.wa.BeginRegistration(wu,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithExclusions(webauthn.Credentials(wu.creds).CredentialDescriptors()),
	)
	if err != nil {
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
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	cred, err := s.wa.FinishRegistration(wu, *sd, r)
	if err != nil {
		http.Error(w, "Passkey-Registrierung fehlgeschlagen.", http.StatusBadRequest)
		return
	}
	name := deviceName(r.UserAgent())
	if err := s.store.AddWebauthnCredential(r.Context(), user.ID, fromWACred(cred, name)); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.audit(r, "passkey_add", "user", user.ID, name)
	writeJSON(w, map[string]string{"status": "ok"})
}

// ---- login ceremony (discoverable / usernameless, public) ---------------

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	assertion, sd, err := s.wa.BeginDiscoverableLogin()
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.saveWASession(w, r, sd)
	writeJSON(w, assertion)
}

func (s *Server) handlePasskeyLoginFinish(w http.ResponseWriter, r *http.Request) {
	sd, ok := s.loadWASession(r)
	if !ok {
		http.Error(w, "Challenge abgelaufen. Bitte erneut versuchen.", http.StatusBadRequest)
		return
	}
	s.clearWASession(w, r)

	rlKey := s.clientIP(r)
	if s.logins.blocked(r.Context(), rlKey) {
		http.Error(w, "Zu viele Fehlversuche. Bitte später erneut versuchen.", http.StatusTooManyRequests)
		return
	}

	var loggedIn *models.User
	handler := func(_, userHandle []byte) (webauthn.User, error) {
		u, err := s.store.UserByWebauthnHandle(r.Context(), userHandle)
		if err != nil {
			return nil, err
		}
		wu, err := s.webauthnUserFor(r, u)
		if err != nil {
			return nil, err
		}
		loggedIn = u
		return wu, nil
	}
	cred, err := s.wa.FinishDiscoverableLogin(handler, *sd, r)
	if err != nil || loggedIn == nil {
		s.logins.fail(r.Context(), rlKey)
		s.auditLogin(r, "", "login_passkey_failed", "")
		http.Error(w, "Anmeldung mit Passkey fehlgeschlagen.", http.StatusUnauthorized)
		return
	}
	s.logins.reset(r.Context(), rlKey)
	_ = s.store.TouchWebauthnCredential(r.Context(), cred.ID, cred.Authenticator.SignCount)
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
