package server

import (
	"errors"
	"net/http"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/totp"
)

// recoveryCodeCount is how many one-time recovery codes are issued.
const recoveryCodeCount = 10

// ---- Sensitive-action rate limiting (per user) --------------------------

func acctLimitKey(userID int64) string { return "acct:" + itoa64(userID) }

// sensitiveBlocked reports (and flashes/redirects) whether the per-user limiter
// for password / 2FA verification is currently tripped.
func (s *Server) sensitiveBlocked(w http.ResponseWriter, r *http.Request, userID int64, redirectTo string) bool {
	if s.logins.blocked(r.Context(), acctLimitKey(userID)) {
		metrics.Inc(metrics.RateLimitTrips)
		s.audit(r, "rate_limited", "user", userID, "zu viele Versuche bei sensibler Aktion")
		s.setFlash(w, r, "error", "Zu viele Versuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, redirectTo)
		return true
	}
	return false
}

func (s *Server) sensitiveAdmit(w http.ResponseWriter, r *http.Request, userID int64, redirectTo string) bool {
	allowed, failed := s.admitVerification(w, r, acctLimitKey(userID), loginMaxFails, loginWindow)
	if failed {
		return false
	}
	if !allowed {
		metrics.Inc(metrics.RateLimitTrips)
		s.setFlash(w, r, "error", "Zu viele Versuche. Bitte in einigen Minuten erneut versuchen.")
		redirect(w, r, redirectTo)
	}
	return allowed
}

func (s *Server) sensitiveReset(r *http.Request, userID int64) {
	s.logins.reset(r.Context(), acctLimitKey(userID))
}

// passwordTooLong rejects values that exceed bcrypt's byte limit before
// consuming an admission attempt. Character-count validation is insufficient
// for multibyte passwords; existing passwords need no new complexity check.
func (s *Server) passwordTooLong(w http.ResponseWriter, r *http.Request, password string) bool {
	if len(password) <= 72 {
		return false
	}
	s.setFlash(
		w,
		r,
		"error",
		"Passwort darf höchstens 72 Byte lang sein.",
	)
	return true
}

// ---- Forced / voluntary password change ---------------------------------

func (s *Server) handleAccountPasswordForm(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	data := s.newPage(w, r, "Passwort ändern", "profile")
	data["Forced"] = user.MustChangePassword
	s.render(w, r, "account_password", data)
}

func (s *Server) handleAccountPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	current := r.FormValue("current_password")
	if s.passwordTooLong(w, r, current) {
		redirect(w, r, "/account/password")
		return
	}
	user := userFromCtx(r)
	next := r.FormValue("new_password")

	if next != r.FormValue("new_password_confirm") {
		s.setFlash(w, r, "error", "Die beiden neuen Passwörter stimmen nicht überein.")
		redirect(w, r, "/account/password")
		return
	}
	if current == next {
		s.setFlash(w, r, "error", "Das neue Passwort darf nicht mit dem aktuellen Passwort übereinstimmen.")
		redirect(w, r, "/account/password")
		return
	}
	if msg := passwordPolicyError(next); msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, "/account/password")
		return
	}
	if !s.sensitiveAdmit(w, r, user.ID, "/account/password") {
		return
	}
	currentToken := ""
	if c, err := s.cookie(r, sessionCookie); err == nil {
		currentToken = c.Value
	}
	token, err := s.store.ChangePassword(r.Context(), store.PasswordChange{
		UserID: user.ID, CurrentPassword: current, NewPassword: next, CurrentToken: currentToken,
		TTL: sessionTTL, AbsoluteTTL: sessionAbsoluteTTL, UserAgent: r.UserAgent(), IP: s.clientIP(r),
	})
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Aktuelles Passwort oder Sitzung ist nicht mehr gültig.")
		redirect(w, r, "/account/password")
		return
	}
	if err != nil {
		s.serverError(w, "password change", err)
		return
	}
	s.sensitiveReset(r, user.ID)
	s.setCookie(w, r, &http.Cookie{Name: sessionCookie, Value: token, MaxAge: int(sessionTTL.Seconds())})
	s.clearPending2FA(w, r)
	s.setFlash(w, r, "success", "Passwort geändert. Andere Sitzungen wurden beendet.")
	redirect(w, r, "/profile")
}

// ---- Two-factor authentication (TOTP) -----------------------------------

// twoFactorRetryPath is where a failed setup confirmation returns to: the same
// setup page, but keeping the pending seed the user has just scanned.
const twoFactorRetryPath = "/account/2fa?retry=1"

// handleTwoFactor shows the 2FA setup / status page. When 2FA is not yet
// enabled it generates (and persists as pending) a secret to display.
//
// Every fresh visit mints a NEW pending seed. The seed used to be stored once
// and shown again on every visit, so anyone who once glimpsed the setup page —
// this GET has no step-up — held the very factor the user enrolled later. Only
// the redirect back from a failed confirmation (?retry=1) keeps the seed, so a
// mistyped code or password does not force a rescan.
func (s *Server) handleTwoFactor(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	// 2FA management (enabled state) now lives inline on the Einstellungen
	// overview; this page only drives the setup flow for users who haven't
	// enabled it yet.
	if user.TotpEnabled {
		redirect(w, r, "/profile")
		return
	}
	data := s.newPage(w, r, "Zwei‑Faktor einrichten", "profile")
	data["Enabled"] = false
	secret := ""
	if r.URL.Query().Get("retry") == "1" {
		var err error
		secret, err = s.store.GetTotpSecret(r.Context(), user.ID)
		if err != nil {
			// A read/decrypt failure must surface, not be masked by minting a
			// fresh secret and overwriting the stored one on a failed read.
			s.serverError(w, "2fa setup: load secret", err)
			return
		}
	}
	if secret == "" {
		var err error
		secret, err = totp.GenerateSecret()
		if err != nil {
			s.serverError(w, "2fa setup: generate secret", err)
			return
		}
		if err := s.store.SetTotp(r.Context(), user.ID, false, secret); err != nil {
			s.serverError(w, "2fa setup: persist secret", err)
			return
		}
	}
	data["Secret"] = secret
	data["URI"] = totp.ProvisioningURI(secret, user.Username, "Treckrr")
	s.render(w, r, "account_2fa", data)
}

// handleTwoFactorQR streams the setup QR code as a PNG for the pending secret.
func (s *Server) handleTwoFactorQR(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user.TotpEnabled {
		s.notFound(w, r) // QR only relevant during setup
		return
	}
	secret, err := s.store.GetTotpSecret(r.Context(), user.ID)
	if err != nil || secret == "" {
		s.notFound(w, r)
		return
	}
	png, err := qrPNG(totp.ProvisioningURI(secret, user.Username, "Treckrr"))
	if err != nil {
		s.serverError(w, "2fa: render QR", err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleTwoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	// Failures return to the setup page WITH the pending seed (retry), so the
	// user does not have to rescan the QR code after a typo.
	if s.passwordTooLong(w, r, r.FormValue("password")) {
		redirect(w, r, twoFactorRetryPath)
		return
	}
	if s.tooLong(
		w,
		r,
		"Code",
		r.FormValue("code"),
		maxNameLen,
	) {
		redirect(w, r, twoFactorRetryPath)
		return
	}
	user := userFromCtx(r)
	secret, err := s.store.GetTotpSecret(r.Context(), user.ID)
	if err != nil || secret == "" {
		s.setFlash(w, r, "error", "Kein ausstehendes 2FA‑Geheimnis. Bitte erneut starten.")
		redirect(w, r, "/account/2fa")
		return
	}
	if !s.sensitiveAdmit(w, r, user.ID, twoFactorRetryPath) {
		return
	}
	// Step-up: enabling a second factor requires re-entering the password, so a
	// hijacked session can't silently enroll one (SH-02).
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.setFlash(w, r, "error", "Passwort falsch – Zwei‑Faktor nicht aktiviert.")
		redirect(w, r, twoFactorRetryPath)
		return
	}
	// The matched step is stored with the factor (AcceptedStep), so the code
	// typed here is consumed and cannot be replayed at /login/2fa.
	step, ok := totp.ValidateStep(secret, r.FormValue("code"))
	if !ok {
		s.setFlash(w, r, "error", "Code ungültig. Bitte erneut versuchen.")
		redirect(w, r, twoFactorRetryPath)
		return
	}
	s.sensitiveReset(r, user.ID)
	// Issue recovery codes and show them once.
	s.issueAndShowRecoveryCodes(w, r, &enrollment{secret: secret, step: step}, "Zwei‑Faktor aktiviert. Bitte die Wiederherstellungscodes jetzt sichern – sie werden nur einmal angezeigt.")
}

// enrollment is a confirmed TOTP seed and the time-step its confirmation code
// matched.
type enrollment struct {
	secret string
	step   uint64
}

// verifySecondFactor checks a TOTP code (consuming its time-step, so it cannot
// be replayed) or, failing that, consumes a matching recovery code. It is the
// step-up for actions that could otherwise sidestep an enabled second factor.
func (s *Server) verifySecondFactor(r *http.Request, userID int64, input string) bool {
	if input == "" || len(input) > maxNameLen {
		return false
	}
	secret, err := s.store.GetTotpSecret(r.Context(), userID)
	if err != nil {
		return false
	}
	if step, ok := totp.ValidateStep(secret, input); ok {
		accepted, err := s.store.AcceptTotpStep(r.Context(), userID, step)
		return err == nil && accepted
	}
	return auth.LooksLikeRecoveryCode(input) && s.consumeRecovery(r, userID, input)
}

// handleRecoveryRegenerate creates a fresh set of recovery codes (invalidating
// the old ones), after confirming the account password.
func (s *Server) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if s.passwordTooLong(w, r, r.FormValue("password")) {
		redirect(w, r, "/account/2fa")
		return
	}
	user := userFromCtx(r)
	if !user.TotpEnabled {
		redirect(w, r, "/account/2fa")
		return
	}
	if !s.sensitiveAdmit(w, r, user.ID, "/account/2fa") {
		return
	}
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.setFlash(w, r, "error", "Passwort falsch – Codes nicht neu erstellt.")
		redirect(w, r, "/account/2fa")
		return
	}
	s.sensitiveReset(r, user.ID)
	s.issueAndShowRecoveryCodes(w, r, nil, "Neue Wiederherstellungscodes erstellt. Alte Codes sind ungültig. Bitte jetzt sichern.")
}

// issueAndShowRecoveryCodes generates, stores and then renders a fresh set of
// recovery codes exactly once. A non-nil enroll also enables that factor.
func (s *Server) issueAndShowRecoveryCodes(w http.ResponseWriter, r *http.Request, enroll *enrollment, notice string) {
	plain, hashes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		s.serverError(w, "2fa: generate recovery codes", err)
		return
	}
	userID := userFromCtx(r).ID
	if enroll != nil {
		err = s.store.ConfigureTwoFactor(r.Context(), store.TwoFactorChange{
			UserID: userID, Enabled: true, Secret: enroll.secret, RecoveryHashes: hashes, AcceptedStep: enroll.step,
		})
	} else {
		err = s.store.ReplaceRecoveryCodes(r.Context(), userID, hashes)
	}
	if err != nil {
		s.serverError(w, "2fa: store recovery codes", err)
		return
	}
	data := s.newPage(w, r, "Zwei‑Faktor", "profile")
	data["Enabled"] = true
	data["NewCodes"] = plain
	data["RecoveryRemaining"] = len(plain)
	data["Notice"] = notice
	s.render(w, r, "account_2fa", data)
}

func (s *Server) handleTwoFactorDisable(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if s.passwordTooLong(w, r, r.FormValue("password")) {
		redirect(w, r, "/account/2fa")
		return
	}
	user := userFromCtx(r)
	if !s.sensitiveAdmit(w, r, user.ID, "/account/2fa") {
		return
	}
	// Require the current password AND the second factor itself to disable 2FA:
	// with the password alone, a hijacked session plus a phished password could
	// strip the factor that exists to stop exactly that.
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.setFlash(w, r, "error", "Passwort oder Code falsch – 2FA nicht deaktiviert.")
		redirect(w, r, "/account/2fa")
		return
	}
	if user.TotpEnabled && !s.verifySecondFactor(r, user.ID, r.FormValue("code")) {
		s.audit(r, "2fa_disable_denied", "user", user.ID, "Zwei-Faktor-Code falsch")
		s.setFlash(w, r, "error", "Passwort oder Code falsch – 2FA nicht deaktiviert.")
		redirect(w, r, "/account/2fa")
		return
	}
	s.sensitiveReset(r, user.ID)
	if err := s.store.ConfigureTwoFactor(r.Context(), store.TwoFactorChange{UserID: user.ID}); err != nil {
		s.serverError(w, "2fa disable: clear totp", err)
		return
	}
	s.setFlash(w, r, "success", "Zwei‑Faktor‑Authentifizierung deaktiviert.")
	redirect(w, r, "/profile")
}

// ---- Session management --------------------------------------------------

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	token := r.FormValue("token")
	if s.tooLong(w, r, "Token", token, maxNameLen) {
		redirect(w, r, "/profile")
		return
	}
	user := userFromCtx(r)
	// A DELETE that matches nothing is not an error, so distinguish it explicitly:
	// reporting "beendet" (and writing an append-only audit record) for a session
	// that was already gone would misstate what happened.
	switch deleted, err := s.store.DeleteSessionForUser(r.Context(), user.ID, token); {
	case err != nil:
		s.setFlash(w, r, "error", "Sitzung konnte nicht beendet werden.")
	case !deleted:
		s.setFlash(w, r, "info", "Diese Sitzung ist bereits beendet.")
	default:
		s.audit(r, "session_revoke", "user", user.ID, "")
		s.setFlash(w, r, "success", "Sitzung beendet.")
	}
	redirect(w, r, "/profile")
}

func (s *Server) handleSessionRevokeOthers(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	current := ""
	if c, err := s.cookie(r, sessionCookie); err == nil {
		current = c.Value
	}
	if err := s.store.DeleteUserSessionsExcept(r.Context(), user.ID, current); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else {
		s.audit(r, "session_revoke_others", "user", user.ID, "")
		s.setFlash(w, r, "success", "Alle anderen Sitzungen wurden beendet.")
	}
	redirect(w, r, "/profile")
}
