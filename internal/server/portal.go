package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// resolvePortalShare applies the same bounded, rate-limited token resolution to
// every public portal action. Only token hashes ever reach the store.
func (s *Server) resolvePortalShare(w http.ResponseWriter, r *http.Request) (shareID, neighborID, yearID int64, token string, ok bool) {
	token = r.PathValue("token")
	if token == "" || len(token) > maxBelegShareTokenLen {
		s.notFound(w, r)
		return
	}
	clientIP := s.clientIP(r)
	if s.logins.shareBlocked(r.Context(), clientIP) {
		s.notFound(w, r)
		return
	}
	if strings.Contains(token, ".") {
		neighborID, yearID, ok = s.verifyLegacyBelegShare(token)
	} else if s.store != nil {
		var err error
		shareID, neighborID, yearID, ok, err = s.store.ResolveBelegShareAccess(r.Context(), store.HashToken(token))
		if err != nil {
			s.serverError(w, "portal: resolve", err)
			return 0, 0, 0, token, false
		}
	}
	if !ok {
		s.logins.shareMiss(r.Context(), clientIP)
		s.notFound(w, r)
		return 0, 0, 0, token, false
	}
	return shareID, neighborID, yearID, token, true
}

// handleSharedInvoicePDF serves the immutable invoice through the same
// revocable portal credential and records the download event.
func (s *Server) handleSharedInvoicePDF(w http.ResponseWriter, r *http.Request) {
	shareID, neighborID, yearID, _, ok := s.resolvePortalShare(w, r)
	if !ok {
		return
	}
	iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID)
	if err != nil || iv.Content == nil {
		s.notFound(w, r)
		return
	}
	blob, err := pdf.RenderInvoice(&iv)
	if err != nil {
		s.serverError(w, "portal pdf", err)
		return
	}
	if err := s.store.RecordBelegShareEvent(r.Context(), shareID, neighborID, yearID, "download"); err != nil {
		s.serverError(w, "portal pdf event", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="Rechnung_`+sanitizeFilename(iv.Number)+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(blob)
}

// handlePortalFeedback records communication against the immutable snapshot;
// it never edits bookings, ledger entries or invoice values.
func (s *Server) handlePortalFeedback(w http.ResponseWriter, r *http.Request) {
	shareID, neighborID, yearID, token, ok := s.resolvePortalShare(w, r)
	if !ok {
		return
	}
	if shareID == 0 { // legacy links have no revocable row to bind a response to
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Rückmeldung konnte nicht verarbeitet werden.")
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if utf8.RuneCountInString(message) > maxNoteLen {
		s.badRequest(w, "Die Nachricht ist zu lang.")
		return
	}
	if raw := strings.TrimSpace(r.FormValue("line")); raw != "" && utf8.RuneCountInString(raw) > maxNameLen {
		s.badRequest(w, "Ungültige Rechnungsposition.")
		return
	}
	status := r.FormValue("status")
	if status != "confirmed" && status != "disputed" {
		s.badRequest(w, "Ungültige Rückmeldung.")
		return
	}
	if status == "disputed" && message == "" {
		s.badRequest(w, "Bitte den Einwand kurz beschreiben.")
		return
	}
	if s.store == nil {
		s.notFound(w, r)
		return
	}
	iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID)
	if err != nil || iv.Content == nil {
		s.notFound(w, r)
		return
	}
	var linePosition *int
	if raw := strings.TrimSpace(r.FormValue("line")); raw != "" {
		idx, err := strconv.Atoi(raw)
		if err != nil || idx < 0 || idx >= len(iv.Content.Lines) {
			s.badRequest(w, "Ungültige Rechnungsposition.")
			return
		}
		position := idx + 1
		linePosition = &position
	}
	if status == "confirmed" {
		message, linePosition = "", nil
	}
	err = s.store.CreateBelegFeedback(r.Context(), shareID, iv.ID, neighborID, yearID,
		iv.Content.Hash, status, linePosition, message)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "portal feedback", err)
		return
	}
	redirect(w, r, "/s/portal/"+token+"?feedback="+status)
}
