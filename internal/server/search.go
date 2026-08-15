package server

import (
	"net/http"
	"strings"
	"unicode/utf8"
)

// handleSearchAPI powers the global search / command palette: it returns JSON
// hits across neighbors, invoices and gespanne for a query of at least two
// characters (shorter queries return an empty list to avoid noise).
func (s *Server) handleSearchAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(q) < 2 {
		writeJSON(w, []any{})
		return
	}
	if utf8.RuneCountInString(q) > maxNameLen {
		q = string([]rune(q)[:maxNameLen])
	}
	results, err := s.store.Search(r.Context(), q, 6)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	writeJSON(w, results)
}
