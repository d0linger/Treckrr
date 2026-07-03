package server

import "net/http"

const themeCookie = "treckrr_theme"

// themeFromCookie returns the persisted color theme ("light", "dark" or "auto").
func themeFromCookie(r *http.Request) string {
	if c, err := r.Cookie(themeCookie); err == nil {
		switch c.Value {
		case "light", "dark", "auto":
			return c.Value
		}
	}
	return "auto"
}

// handleTheme persists the chosen color theme and returns to the previous page.
func (s *Server) handleTheme(w http.ResponseWriter, r *http.Request) {
	value := r.URL.Query().Get("set")
	switch value {
	case "light", "dark", "auto":
	default:
		value = "auto"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     themeCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	target := r.Header.Get("Referer")
	if target == "" {
		target = "/profile"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
