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
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
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
	c, err := s.cookie(r, waCookie)
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

// handlePasskeyDelete removes only a credential belonging to the current user.
// Missing or foreign credentials are treated as an already-completed deletion.
func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	_, err = s.store.DeleteWebauthnCredential(r.Context(), user.ID, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Already gone (double-submit, stale page, or someone else's id): the
		// desired end state — no such passkey for this user — already holds, so
		// treat it as success and don't write an audit line for a no-op delete.
		s.setFlash(w, r, "success", "Passkey entfernt.")
	case err != nil:
		s.setFlash(w, r, "error", "Passkey konnte nicht entfernt werden.")
	default:
		s.setFlash(w, r, "success", "Passkey entfernt.")
	}
	redirect(w, r, "/profile")
}

// ---- registration ceremony (authenticated) ------------------------------

func (s *Server) handlePasskeyRegisterBegin(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	// Step-up: adding a durable passkey requires re-entering the password, so a
	// hijacked session can't silently enroll one (SH-02). With TOTP enabled the
	// second factor is required as well: a passkey login never asks for TOTP, so
	// a password-only step-up let a hijacked session plus a known password mint a
	// permanent, TOTP-free way back in. The body is bounded by limitBody.
	var body struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Ungültige Anfrage.", http.StatusBadRequest)
		return
	}
	// This is a password-verification endpoint like the 2FA and change-password
	// steps, and it must be throttled like them: unbounded, a hijacked session
	// could brute-force the account password here (and drive one bcrypt hash per
	// request while doing it). Same per-user limiter, JSON-shaped response.
	allowed, failed := s.admitVerification(w, r, acctLimitKey(user.ID), loginMaxFails, loginWindow)
	if failed {
		return
	}
	if !allowed {
		s.audit(r, "rate_limited", "user", user.ID, "zu viele Versuche bei sensibler Aktion")
		http.Error(w, "Zu viele Versuche. Bitte in einigen Minuten erneut versuchen.",
			http.StatusTooManyRequests)
		return
	}
	// One message for either failure when 2FA is on, so the endpoint is no
	// password oracle for someone holding only the session.
	denied := "Passwort falsch."
	if user.TotpEnabled {
		denied = "Passwort oder Zwei‑Faktor‑Code falsch."
	}
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, body.Password); err != nil {
		s.audit(r, "passkey_add_denied", "user", user.ID, "Passwort falsch")
		http.Error(w, denied, http.StatusForbidden)
		return
	}
	if user.TotpEnabled && !s.verifySecondFactor(r, user.ID, strings.TrimSpace(body.Code)) {
		s.audit(r, "passkey_add_denied", "user", user.ID, "Zwei-Faktor-Code falsch")
		http.Error(w, denied, http.StatusForbidden)
		return
	}
	s.sensitiveReset(r, user.ID)
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
	writeJSON(w, map[string]string{"status": "ok"})
}

// ---- login ceremony (discoverable / usernameless, public) ---------------

func (s *Server) handlePasskeyLoginBegin(w http.ResponseWriter, r *http.Request) {
	// Rate-limit begin too: it persists a server-side ceremony row (SH-03), so a
	// blocked IP must not be able to spam ceremony creation.
	//
	// Checking the login limiter alone was not enough: that counter only rises on
	// FAILED logins, so a client that never attempts one never trips it and could
	// create ceremony rows without bound. The ceremony limiter below charges every
	// begin — successful ones included — because the row is created either way.
	ip := s.limiterIP(r)
	if s.logins.blocked(r.Context(), ip) {
		s.auditLogin(r, "", "login_passkey_failed", "Rate-Limit: zu viele Fehlversuche")
		http.Error(w, "Zu viele Fehlversuche. Bitte später erneut versuchen.", http.StatusTooManyRequests)
		return
	}
	allowed, err := s.logins.allowCeremonyBegin(r.Context(), ip)
	if err != nil {
		// Fail closed: the next step needs the same database, so there is nothing
		// to gain by letting this through — and everything to lose, since this is
		// the unauthenticated path that persists a row.
		slog.Error("passkey begin limiter unavailable", "ip", sanitizeLog(ip), "err", sanitizeLog(err.Error()))
		http.Error(w, "Dienst vorübergehend nicht verfügbar.", http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		s.auditLogin(r, "", "login_passkey_failed", "Rate-Limit: zu viele Anfragen")
		w.Header().Set("Retry-After", strconv.Itoa(int(ceremonyWindow.Seconds())))
		http.Error(w, "Zu viele Anfragen. Bitte später erneut versuchen.", http.StatusTooManyRequests)
		return
	}
	assertion, sd, err := s.wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired), // T-03
	)
	if err != nil {
		slog.Warn("passkey login begin failed",
			"ip", sanitizeLog(ip), "reason", sanitizeLog(webauthnErrReason(err)))
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if err := s.saveWASession(w, r, sd); err != nil {
		slog.Error("passkey login begin: save ceremony failed", "ip", sanitizeLog(ip), "err", sanitizeLog(err.Error()))
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

	rlKey := s.limiterIP(r)
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
	s.completePasskeyLogin(w, r, loggedIn, cred, rlKey)
}

// completePasskeyLogin finishes a verified passkey assertion: clone check,
// counter persistence, audit and session. The per-IP failure bucket is left
// alone on success — it is shared by every account behind the address, so
// clearing it would let a working login launder other accounts' failures.
func (s *Server) completePasskeyLogin(w http.ResponseWriter, r *http.Request, user *models.User, cred *webauthn.Credential, rlKey string) {
	// Clone detection: go-webauthn flags a signature counter that did not
	// advance (never for counter-less synced authenticators, which stay at 0).
	// The persisted counter is then only advanced when it is still below the
	// asserted one, so two racing assertions cannot let the lower count win and
	// hide a later regression; losing that race counts as a regression too.
	// Either way the login is refused: counter-based clone detection that only
	// logs lets a cloned hardware key keep working unnoticed.
	advanced := false
	if !cred.Authenticator.CloneWarning {
		var err error
		advanced, err = s.store.TouchWebauthnCredential(r.Context(), cred.ID, cred.Authenticator.SignCount, cred.Flags.BackupState)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
	}
	if !advanced {
		s.logins.fail(r.Context(), rlKey)
		slog.Warn("passkey login refused: possible clone (signature counter did not advance)",
			"user", sanitizeLog(user.Username), "ip", sanitizeLog(s.clientIP(r)))
		s.auditLogin(r, user.Username, "login_passkey_clone_warning",
			"Signaturzähler nicht gestiegen – möglicher Klon, Anmeldung abgelehnt")
		http.Error(w, "Anmeldung mit diesem Passkey abgelehnt: möglicher Klon erkannt. "+
			"Bitte mit Passwort anmelden und den Passkey in den Einstellungen prüfen.", http.StatusUnauthorized)
		return
	}
	// A completed login clears the ceremony budget, so a user who retried a few
	// times doesn't carry the count into their next login.
	s.logins.ceremonyReset(r.Context(), rlKey)
	s.auditLogin(r, user.Username, "login_passkey", "")
	if !s.startSession(w, r, user) {
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
