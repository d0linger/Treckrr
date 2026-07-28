package server

import (
	"fmt"
	"net/http"
	"unicode/utf8"
)

// Input-length ceilings (in code points, matching the German "Zeichen"
// wording) for free-text fields that are persisted to unbounded Postgres TEXT
// columns. They guard against oversized-payload abuse on new writes; existing
// rows are unaffected.
const (
	maxNameLen = 100 // short names, labels, identifiers, categories
	maxNoteLen = 500 // notes, descriptions, cancellation reasons
)

// lenError returns a German over-limit message when value exceeds max code
// points, or "" when it is within bounds. field is the user-facing field name.
// Handlers that surface validation failures as a returned string use this
// directly; those that flash-and-redirect use tooLong.
func lenError(field, value string, max int) string {
	if utf8.RuneCountInString(value) > max {
		return fmt.Sprintf("%s darf höchstens %d Zeichen lang sein.", field, max)
	}
	return ""
}

// tooLong flashes an over-limit error for field and returns true when value
// exceeds max code points. The caller is responsible for the redirect.
func (s *Server) tooLong(w http.ResponseWriter, r *http.Request, field, value string, max int) bool {
	if msg := lenError(field, value, max); msg != "" {
		s.setFlash(w, r, "error", msg)
		return true
	}
	return false
}
