package server

import (
	"net/http"
	"net/url"
	"strings"
)

const themeCookie = "treckrr_theme"

// themeFromCookie returns the persisted color theme ("light", "dark" or "auto").
func (s *Server) themeFromCookie(r *http.Request) string {
	if c, err := s.cookie(r, themeCookie); err == nil {
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
	// Reject backslashes in path to prevent potential open redirects via browser normalization (e.g. /\attacker.com).
	if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(u.Path, "//") || strings.Contains(u.Path, "\\") {
		return fallback
	}
	// Control characters are refused outright, decoded or not: browsers strip a
	// tab or newline from a Location value, so "/%09/evil.com" — which passes the
	// checks above once decoded to "/\t/evil.com" — would be followed as
	// "//evil.com".
	if hasControlChar(u.Path) || hasControlChar(u.RawQuery) {
		return fallback
	}
	// Emit the ESCAPED path, never the decoded one, so nothing the Referer
	// percent-encoded reaches the Location header raw.
	target := u.EscapedPath()
	if len(target) == 0 || target[0] != '/' ||
		(len(target) > 1 && (target[1] == '/' || target[1] == '\\')) ||
		strings.Contains(target[1:], "\\") {
		return fallback
	}
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target
}

// hasControlChar reports whether s contains an ASCII control character or DEL.
func hasControlChar(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}
