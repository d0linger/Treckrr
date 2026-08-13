package server

import (
	"errors"
	"log"
	"net/http"
	"net/mail"
	"strings"
	"unicode/utf8"

	"treckrr/internal/models"
	"treckrr/internal/store"
)

// Input-length ceilings on the admin user endpoints (defense against oversized-
// payload abuse). Username is bounded by code points to match the "Zeichen"
// wording; e-mail by octets, the natural unit for RFC 5321's 254-char limit.
const (
	maxUsernameLen = 100
	maxEmailLen    = 254
)

// validRole reports whether the given role string is one of the known roles.
func validRole(role string) bool {
	switch role {
	case models.RoleAdmin, models.RoleEditor, models.RoleViewer:
		return true
	default:
		return false
	}
}

// passwordPolicyError validates a password against the policy and returns a
// German error message, or "" when the password is acceptable.
func passwordPolicyError(pw string) string {
	if len(pw) < 8 {
		return "Passwort muss mindestens 8 Zeichen haben."
	}
	// bcrypt silently truncates input beyond 72 bytes, so anything longer would
	// have unused tail bytes (and, with GenerateFromPassword, error out). Reject
	// it explicitly instead of hashing a silently-shortened password.
	if len(pw) > 72 {
		return "Passwort darf höchstens 72 Zeichen lang sein."
	}
	var hasLetter, hasDigit bool
	for _, c := range pw {
		switch {
		case c >= '0' && c <= '9':
			hasDigit = true
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		}
	}
	if !hasLetter || !hasDigit {
		return "Passwort muss Buchstaben und Ziffern enthalten."
	}
	return ""
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Benutzerverwaltung", "admin")
	data["Users"] = users
	data["Roles"] = []string{models.RoleAdmin, models.RoleEditor, models.RoleViewer}
	s.render(w, r, "admin", data)
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := r.FormValue("role")
	if !validRole(role) {
		role = models.RoleEditor
	}
	if username == "" {
		s.setFlash(w, r, "error", "Benutzername ist erforderlich.")
		redirect(w, r, "/admin/users")
		return
	}
	if utf8.RuneCountInString(username) > maxUsernameLen {
		s.setFlash(w, r, "error", "Benutzername darf höchstens 100 Zeichen lang sein.")
		redirect(w, r, "/admin/users")
		return
	}
	if msg := passwordPolicyError(password); msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, "/admin/users")
		return
	}
	newID, err := s.store.CreateUser(r.Context(), username, password, role)
	if err != nil {
		s.setFlash(w, r, "error", "Anlegen fehlgeschlagen (Benutzername bereits vergeben?).")
		redirect(w, r, "/admin/users")
		return
	}
	if r.FormValue("force_change") == "on" {
		_ = s.store.SetMustChangePassword(r.Context(), newID, true)
	}
	s.audit(r, "create", "user", newID, username+" ("+role+")")
	s.setFlash(w, r, "success", "Benutzer angelegt.")
	redirect(w, r, "/admin/users")
}

func (s *Server) handleUserPassword(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	password := r.FormValue("password")
	if msg := passwordPolicyError(password); msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, "/admin/users")
		return
	}
	if err := s.store.UpdatePassword(r.Context(), id, password); err != nil {
		s.setFlash(w, r, "error", "Änderung fehlgeschlagen.")
		redirect(w, r, "/admin/users")
		return
	}
	// Force the user to change this admin-set password at next login.
	_ = s.store.SetMustChangePassword(r.Context(), id, r.FormValue("force_change") == "on")
	// Terminate the target user's sessions so the reset takes effect immediately.
	// Auth is by session token, not password, so this is load-bearing: a failure
	// must surface rather than be reported to the admin as success.
	if err := s.store.DeleteUserSessionsExcept(r.Context(), id, ""); err != nil {
		s.serverError(w, "password reset: revoke sessions", err)
		return
	}
	s.audit(r, "password_reset", "user", id, "durch Admin; Sitzungen beendet")
	s.setFlash(w, r, "success", "Passwort gesetzt. Bestehende Sitzungen wurden beendet.")
	redirect(w, r, "/admin/users")
}

// handleUserRole assigns a role to a user, protecting the last admin.
func (s *Server) handleUserRole(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	role := r.FormValue("role")
	if !validRole(role) {
		s.setFlash(w, r, "error", "Unbekannte Rolle.")
		redirect(w, r, "/admin/users")
		return
	}
	// Change the role and protect the last admin atomically (SH-04): the check and
	// the update run in one transaction, failing closed on any error.
	switch err := s.store.SetRoleSafe(r.Context(), id, role); {
	case errors.Is(err, store.ErrLastAdmin):
		s.setFlash(w, r, "error", "Der letzte Administrator kann nicht herabgestuft werden.")
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Benutzer nicht gefunden.")
	case err != nil:
		log.Printf("set role user %d failed: %v", id, sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Änderung fehlgeschlagen.")
	default:
		// Rotate privileges: end the user's sessions so the new role takes
		// effect on their next (re-authenticated) session.
		_ = s.store.DeleteUserSessionsExcept(r.Context(), id, "")
		s.audit(r, "set_role", "user", id, role+"; Sitzungen beendet")
		s.setFlash(w, r, "success", "Rolle aktualisiert. Sitzungen des Benutzers wurden beendet.")
	}
	redirect(w, r, "/admin/users")
}

// handleUserUpdate changes a user's username and e-mail. The username is unique,
// so a clash is reported rather than swallowed.
func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	username := trimmed(r, "username")
	email := trimmed(r, "email")
	if username == "" {
		s.setFlash(w, r, "error", "Benutzername darf nicht leer sein.")
		redirect(w, r, "/admin/users")
		return
	}
	// Bound input size on this admin endpoint (defense against oversized-payload
	// abuse); the username column and RFC 5321's 254-char address limit are the
	// natural ceilings.
	if utf8.RuneCountInString(username) > maxUsernameLen {
		s.setFlash(w, r, "error", "Benutzername darf höchstens 100 Zeichen lang sein.")
		redirect(w, r, "/admin/users")
		return
	}
	if len(email) > maxEmailLen {
		s.setFlash(w, r, "error", "E‑Mail‑Adresse darf höchstens 254 Zeichen lang sein.")
		redirect(w, r, "/admin/users")
		return
	}
	// E-mail is optional, but if present it must be a valid address (the client
	// type="email" is bypassable, so validate server-side too).
	if email != "" {
		if _, err := mail.ParseAddress(email); err != nil {
			s.setFlash(w, r, "error", "Ungültige E‑Mail‑Adresse.")
			redirect(w, r, "/admin/users")
			return
		}
	}
	if err := s.store.UpdateUserAccount(r.Context(), id, username, email); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen (Benutzername bereits vergeben?).")
		redirect(w, r, "/admin/users")
		return
	}
	disp := func(v string) string {
		if v == "" {
			return "(leer)"
		}
		return v
	}
	var parts []string
	if username != target.Username {
		parts = append(parts, "Benutzername: "+target.Username+" → "+username)
	}
	if email != target.Email {
		parts = append(parts, "E‑Mail: "+disp(target.Email)+" → "+disp(email))
	}
	detail := "Zugangsdaten aktualisiert"
	if len(parts) > 0 {
		detail = strings.Join(parts, "; ")
	}
	s.audit(r, "update", "user", id, detail)
	s.setFlash(w, r, "success", "Zugangsdaten aktualisiert.")
	redirect(w, r, "/admin/users")
}

// handleUserResetTotp lets an admin disable & clear a user's 2FA (e.g. when the
// user lost their authenticator device and their recovery codes).
func (s *Server) handleUserResetTotp(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target, err := s.store.GetUser(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetTotp(r.Context(), id, false, ""); err != nil {
		s.setFlash(w, r, "error", "Zurücksetzen fehlgeschlagen.")
		redirect(w, r, "/admin/users")
		return
	}
	_ = s.store.ClearRecoveryCodes(r.Context(), id)
	s.audit(r, "2fa_reset", "user", id, "durch Admin ("+target.Username+")")
	s.setFlash(w, r, "success", "2FA für "+target.Username+" zurückgesetzt. Der Benutzer kann es neu einrichten.")
	redirect(w, r, "/admin/users")
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	current := userFromCtx(r)
	if current.ID == id {
		s.setFlash(w, r, "error", "Sie können sich nicht selbst löschen.")
		redirect(w, r, "/admin/users")
		return
	}
	target, _ := s.store.GetUser(r.Context(), id) // best-effort, for the audit label
	// Delete and protect the last admin atomically (SH-04), failing closed.
	switch err := s.store.DeleteUserSafe(r.Context(), id); {
	case errors.Is(err, store.ErrLastAdmin):
		s.setFlash(w, r, "error", "Der letzte Administrator kann nicht gelöscht werden.")
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Benutzer nicht gefunden.")
	case err != nil:
		log.Printf("delete user %d failed: %v", id, sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	default:
		detail := ""
		if target != nil {
			detail = target.Username
		}
		s.audit(r, "delete", "user", id, detail)
		s.setFlash(w, r, "success", "Benutzer gelöscht.")
	}
	redirect(w, r, "/admin/users")
}
