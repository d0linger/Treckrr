package server

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const savedViewNameMax = 60

// savedBookingQuery rebuilds the booking query from an explicit allowlist. The
// result can safely be stored and later appended only to /buchungen.
func savedBookingQuery(r *http.Request, yearID int64) string {
	in := r.URL.Query()
	out := url.Values{"year": {strconv.FormatInt(yearID, 10)}}
	copyValue := func(key string, valid func(string) bool) {
		value := strings.TrimSpace(in.Get(key))
		if value != "" && valid(value) {
			out.Set(key, value)
		}
	}
	copyValue("neighbor_id", func(v string) bool { id, err := strconv.ParseInt(v, 10, 64); return err == nil && id > 0 })
	copyValue("task", func(v string) bool { return len(v) <= maxNameLen })
	copyValue("direction", func(v string) bool { return v == "in" || v == "out" })
	copyValue("kind", func(v string) bool {
		switch v {
		case "equipment", "labor", "quantity", "fixed", "manual", "transfer":
			return true
		}
		return false
	})
	copyValue("from", func(v string) bool { return !parseDay(v).IsZero() })
	copyValue("to", func(v string) bool { return !parseDay(v).IsZero() })
	copyValue("unit", func(v string) bool { return len(v) <= 30 })
	copyValue("voided", func(v string) bool { return v == "hide" || v == "only" })
	copyValue("sort", func(v string) bool { return v == "date" || v == "cost" || v == "neighbor" })
	copyValue("dir", func(v string) bool { return v == "desc" })
	return out.Encode()
}

// handleSavedViewCreate stores only the server-rebuilt booking filter for the
// authenticated user; paths and arbitrary query keys are never accepted.
func (s *Server) handleSavedViewCreate(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	name := trimmed(r, "name")
	if name == "" || len(name) > savedViewNameMax {
		s.setFlash(w, r, "error", "Bitte einen Namen mit höchstens 60 Zeichen angeben.")
		redirect(w, r, "/buchungen")
		return
	}
	yearID := formInt64(r, "year_id")
	if _, err := s.store.GetBillingYear(r.Context(), yearID); err != nil {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist nicht verfügbar.")
		redirect(w, r, "/buchungen")
		return
	}
	raw := r.FormValue("query")
	values, err := url.ParseQuery(raw)
	if err != nil {
		s.badRequest(w, "Der Filter ist ungültig.")
		return
	}
	filterRequest := r.Clone(r.Context())
	filterRequest.URL = &url.URL{Path: "/buchungen", RawQuery: values.Encode()}
	query := savedBookingQuery(filterRequest, yearID)
	if err := s.store.SaveView(r.Context(), user.ID, "bookings", name, query); err != nil {
		s.serverError(w, "save view", err)
		return
	}
	s.setFlash(w, r, "success", "Ansicht „"+name+"“ gespeichert.")
	redirect(w, r, "/buchungen?"+query)
}

// handleSavedViewDelete removes a saved view only from its owner.
func (s *Server) handleSavedViewDelete(w http.ResponseWriter, r *http.Request) {
	user := userFromCtx(r)
	if user == nil {
		s.notFound(w, r)
		return
	}
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.DeleteSavedView(r.Context(), user.ID, id); err != nil {
		s.serverError(w, "delete view", err)
		return
	}
	s.setFlash(w, r, "success", "Gespeicherte Ansicht gelöscht.")
	redirect(w, r, "/buchungen")
}
