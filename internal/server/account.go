package server

import (
	"net/http"

	"treckrr/internal/auth"
	"treckrr/internal/totp"
)

// recoveryCodeCount is how many one-time recovery codes are issued.
const recoveryCodeCount = 10

// ---- Forced / voluntary password change ---------------------------------

func (s *Server) handleAccountPasswordForm(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	data := s.newPage(w, r, "Passwort ändern", "profile")
	data["Forced"] = user.MustChangePassword
	s.render(w, r, "account_password", data)
}

func (s *Server) handleAccountPasswordSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	user := userFromCtx(r)
	current := r.FormValue("current_password")
	next := r.FormValue("new_password")

	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, current); err != nil {
		s.setFlash(w, "error", "Aktuelles Passwort ist falsch.")
		redirect(w, r, "/account/password")
		return
	}
	if msg := passwordPolicyError(next); msg != "" {
		s.setFlash(w, "error", msg)
		redirect(w, r, "/account/password")
		return
	}
	if err := s.store.UpdatePassword(r.Context(), user.ID, next); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	_ = s.store.SetMustChangePassword(r.Context(), user.ID, false)
	s.audit(r, "password_change", "user", user.ID, "eigenes Passwort")
	s.setFlash(w, "success", "Passwort geändert.")
	redirect(w, r, "/profile")
}

// ---- Two-factor authentication (TOTP) -----------------------------------

// handleTwoFactor shows the 2FA setup / status page. When 2FA is not yet
// enabled it generates (and persists as pending) a secret to display.
func (s *Server) handleTwoFactor(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	data := s.newPage(w, r, "Zwei‑Faktor", "profile")
	data["Enabled"] = user.TotpEnabled

	if !user.TotpEnabled {
		secret, err := s.store.GetTotpSecret(r.Context(), user.ID)
		if err != nil || secret == "" {
			secret, err = totp.GenerateSecret()
			if err != nil {
				http.Error(w, "Interner Fehler", http.StatusInternalServerError)
				return
			}
			if err := s.store.SetTotp(r.Context(), user.ID, false, secret); err != nil {
				http.Error(w, "Interner Fehler", http.StatusInternalServerError)
				return
			}
		}
		data["Secret"] = secret
		data["URI"] = totp.ProvisioningURI(secret, user.Username, "Treckrr")
	} else {
		remaining, err := s.store.CountUnusedRecoveryCodes(r.Context(), user.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		data["RecoveryRemaining"] = remaining
	}
	s.render(w, r, "account_2fa", data)
}

// handleTwoFactorQR streams the setup QR code as a PNG for the pending secret.
func (s *Server) handleTwoFactorQR(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user.TotpEnabled {
		http.NotFound(w, r) // QR only relevant during setup
		return
	}
	secret, err := s.store.GetTotpSecret(r.Context(), user.ID)
	if err != nil || secret == "" {
		http.NotFound(w, r)
		return
	}
	png, err := qrPNG(totp.ProvisioningURI(secret, user.Username, "Treckrr"))
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) handleTwoFactorConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	user := userFromCtx(r)
	secret, err := s.store.GetTotpSecret(r.Context(), user.ID)
	if err != nil || secret == "" {
		s.setFlash(w, "error", "Kein ausstehendes 2FA‑Geheimnis. Bitte erneut starten.")
		redirect(w, r, "/account/2fa")
		return
	}
	if !totp.Validate(secret, r.FormValue("code")) {
		s.setFlash(w, "error", "Code ungültig. Bitte erneut versuchen.")
		redirect(w, r, "/account/2fa")
		return
	}
	if err := s.store.SetTotp(r.Context(), user.ID, true, secret); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.audit(r, "2fa_enable", "user", user.ID, "")
	// Issue recovery codes and show them once.
	s.issueAndShowRecoveryCodes(w, r, user.ID, "Zwei‑Faktor aktiviert. Bitte die Wiederherstellungscodes jetzt sichern – sie werden nur einmal angezeigt.")
}

// handleRecoveryRegenerate creates a fresh set of recovery codes (invalidating
// the old ones), after confirming the account password.
func (s *Server) handleRecoveryRegenerate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	user := userFromCtx(r)
	if !user.TotpEnabled {
		redirect(w, r, "/account/2fa")
		return
	}
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.setFlash(w, "error", "Passwort falsch – Codes nicht neu erstellt.")
		redirect(w, r, "/account/2fa")
		return
	}
	s.audit(r, "2fa_recovery_regenerate", "user", user.ID, "")
	s.issueAndShowRecoveryCodes(w, r, user.ID, "Neue Wiederherstellungscodes erstellt. Alte Codes sind ungültig. Bitte jetzt sichern.")
}

// issueAndShowRecoveryCodes generates, stores and then renders a fresh set of
// recovery codes exactly once.
func (s *Server) issueAndShowRecoveryCodes(w http.ResponseWriter, r *http.Request, userID int64, notice string) {
	plain, hashes, err := auth.GenerateRecoveryCodes(recoveryCodeCount)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if err := s.store.ReplaceRecoveryCodes(r.Context(), userID, hashes); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	user := userFromCtx(r)
	// Require the current password to disable 2FA.
	if _, err := s.store.AuthenticateUser(r.Context(), user.Username, r.FormValue("password")); err != nil {
		s.setFlash(w, "error", "Passwort falsch – 2FA nicht deaktiviert.")
		redirect(w, r, "/account/2fa")
		return
	}
	if err := s.store.SetTotp(r.Context(), user.ID, false, ""); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	_ = s.store.ClearRecoveryCodes(r.Context(), user.ID)
	s.audit(r, "2fa_disable", "user", user.ID, "")
	s.setFlash(w, "success", "Zwei‑Faktor‑Authentifizierung deaktiviert.")
	redirect(w, r, "/profile")
}

// ---- Session management --------------------------------------------------

func (s *Server) handleSessionRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	user := userFromCtx(r)
	if err := s.store.DeleteSessionForUser(r.Context(), user.ID, r.FormValue("token")); err != nil {
		s.setFlash(w, "error", "Sitzung konnte nicht beendet werden.")
	} else {
		s.audit(r, "session_revoke", "user", user.ID, "")
		s.setFlash(w, "success", "Sitzung beendet.")
	}
	redirect(w, r, "/profile")
}

func (s *Server) handleSessionRevokeOthers(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	current := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		current = c.Value
	}
	if err := s.store.DeleteUserSessionsExcept(r.Context(), user.ID, current); err != nil {
		s.setFlash(w, "error", "Aktion fehlgeschlagen.")
	} else {
		s.audit(r, "session_revoke_others", "user", user.ID, "")
		s.setFlash(w, "success", "Alle anderen Sitzungen wurden beendet.")
	}
	redirect(w, r, "/profile")
}
