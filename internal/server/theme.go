package server

import (
	"net/http"
	"net/url"
	"strings"
)

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
	s.setCookie(w, r, &http.Cookie{
		Name:   themeCookie,
		Value:  value,
		MaxAge: 365 * 24 * 3600,
	})
	http.Redirect(w, r, safeReturnPath(r, "/profile"), http.StatusSeeOther)
}

// safeReturnPath returns the Referer as a local, same-origin path (to send the
// user back where they were) or the fallback. It rejects absolute/cross-origin
// URLs to prevent open redirects.
func safeReturnPath(r *http.Request, fallback string) string {
	ref := r.Header.Get("Referer")
	if ref == "" {
		return fallback
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Host != "" && u.Host != r.Host) {
		return fallback
	}
	// Only a local absolute path ("/...", but not "//host" or a scheme).
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") {
		return fallback
	}
	target := u.Path
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target
}
