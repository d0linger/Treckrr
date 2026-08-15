package server

import (
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// handleEntryPrecheck returns a soft warning (empty = none) before a booking is
// saved: implausible hours-per-day, or a same-day duplicate of the same named
// task. It never blocks — the client shows a confirm dialog. Read-only.
func (s *Server) handleEntryPrecheck(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	warn := ""
	if h := parseGermanDecimal(q.Get("hours")); h.GreaterThan(decimal.NewFromInt(24)) {
		warn = h.String() + " Stunden an einem Tag – wirklich speichern?"
	}
	if warn == "" {
		neighborID := formInt64FromQuery(r, "neighbor_id")
		yearID := formInt64FromQuery(r, "year_id")
		task := strings.TrimSpace(q.Get("task_label"))
		date, derr := time.Parse("2006-01-02", strings.TrimSpace(q.Get("entry_date")))
		if neighborID > 0 && yearID > 0 && derr == nil {
			if dup, err := s.store.SimilarEntryExists(r.Context(), neighborID, yearID, date, task); err == nil && dup {
				warn = "Am " + date.Format("02.01.2006") + " ist bereits eine Buchung „" + task + "“ erfasst. Trotzdem speichern?"
			}
		}
	}
	writeJSON(w, map[string]string{"warn": warn})
}

// handleSearchAPI powers the global search / command palette: it returns JSON

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
