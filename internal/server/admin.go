package server

import (
	"net/http"
	"strings"

	"treckrr/internal/models"
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
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
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
		s.setFlash(w, "error", "Benutzername ist erforderlich.")
		redirect(w, r, "/admin/users")
		return
	}
	if msg := passwordPolicyError(password); msg != "" {
		s.setFlash(w, "error", msg)
		redirect(w, r, "/admin/users")
		return
	}
	newID, err := s.store.CreateUser(r.Context(), username, password, role)
	if err != nil {
		s.setFlash(w, "error", "Anlegen fehlgeschlagen (Benutzername bereits vergeben?).")
		redirect(w, r, "/admin/users")
		return
	}
	if r.FormValue("force_change") == "on" {
		_ = s.store.SetMustChangePassword(r.Context(), newID, true)
	}
	s.audit(r, "create", "user", newID, username+" ("+role+")")
	s.setFlash(w, "success", "Benutzer angelegt.")
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
		s.setFlash(w, "error", msg)
		redirect(w, r, "/admin/users")
		return
	}
	if err := s.store.UpdatePassword(r.Context(), id, password); err != nil {
		s.setFlash(w, "error", "Änderung fehlgeschlagen.")
		redirect(w, r, "/admin/users")
		return
	}
	// Force the user to change this admin-set password at next login.
	_ = s.store.SetMustChangePassword(r.Context(), id, r.FormValue("force_change") == "on")
	s.audit(r, "password_reset", "user", id, "durch Admin")
	s.setFlash(w, "success", "Passwort gesetzt.")
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
		s.setFlash(w, "error", "Unbekannte Rolle.")
		redirect(w, r, "/admin/users")
		return
	}
	// Prevent demoting the last remaining admin.
	if role != models.RoleAdmin {
		if target, err := s.store.GetUser(r.Context(), id); err == nil && target.IsAdmin {
			if n, err := s.store.CountAdmins(r.Context()); err == nil && n <= 1 {
				s.setFlash(w, "error", "Der letzte Administrator kann nicht herabgestuft werden.")
				redirect(w, r, "/admin/users")
				return
			}
		}
	}
	if err := s.store.SetRole(r.Context(), id, role); err != nil {
		s.setFlash(w, "error", "Änderung fehlgeschlagen.")
	} else {
		s.audit(r, "set_role", "user", id, role)
		s.setFlash(w, "success", "Rolle aktualisiert.")
	}
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
		s.setFlash(w, "error", "Zurücksetzen fehlgeschlagen.")
		redirect(w, r, "/admin/users")
		return
	}
	_ = s.store.ClearRecoveryCodes(r.Context(), id)
	s.audit(r, "2fa_reset", "user", id, "durch Admin ("+target.Username+")")
	s.setFlash(w, "success", "2FA für "+target.Username+" zurückgesetzt. Der Benutzer kann es neu einrichten.")
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
		s.setFlash(w, "error", "Sie können sich nicht selbst löschen.")
		redirect(w, r, "/admin/users")
		return
	}
	target, err := s.store.GetUser(r.Context(), id)
	if err == nil && target.IsAdmin {
		if n, err := s.store.CountAdmins(r.Context()); err == nil && n <= 1 {
			s.setFlash(w, "error", "Der letzte Administrator kann nicht gelöscht werden.")
			redirect(w, r, "/admin/users")
			return
		}
	}
	if err := s.store.DeleteUser(r.Context(), id); err != nil {
		s.setFlash(w, "error", "Löschen fehlgeschlagen.")
	} else {
		detail := ""
		if target != nil {
			detail = target.Username
		}
		s.audit(r, "delete", "user", id, detail)
		s.setFlash(w, "success", "Benutzer gelöscht.")
	}
	redirect(w, r, "/admin/users")
}
