package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/d0linger/treckrr/internal/store"
)

// feedbackResult accepts only the statuses produced by the public feedback forms.
func feedbackResult(r *http.Request) string {
	switch result := r.URL.Query().Get("feedback"); result {
	case "confirmed", "disputed":
		return result
	default:
		return ""
	}
}

// resolvePortalShare applies the same bounded, rate-limited token resolution to
// every public portal action. Only token hashes ever reach the store.
func (s *Server) resolvePortalShare(w http.ResponseWriter, r *http.Request) (shareID, neighborID, yearID, invoiceID int64, token string, ok bool) {
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
	// Legacy HMAC links encode an account, not a document. Their origin cannot
	// be proved after storno/reissue, so never retarget them to a new invoice.
	if strings.Contains(token, ".") {
		s.logins.shareMiss(r.Context(), clientIP)
		s.notFound(w, r)
		return
	}
	var err error
	shareID, neighborID, yearID, invoiceID, ok, err = s.store.ResolveInvoiceBelegShareAccess(r.Context(), store.HashToken(token))
	if err != nil {
		s.serverError(w, "portal: resolve", err)
		return 0, 0, 0, 0, token, false
	}
	if !ok {
		s.logins.shareMiss(r.Context(), clientIP)
		s.notFound(w, r)
		return 0, 0, 0, 0, token, false
	}
	return
}

// handleSharedInvoicePDF serves the immutable invoice through the same
// revocable portal credential and records the download event.
func (s *Server) handleSharedInvoicePDF(w http.ResponseWriter, r *http.Request) {
	shareID, neighborID, yearID, invoiceID, _, ok := s.resolvePortalShare(w, r)
	if !ok {
		return
	}
	iv, err := s.store.GetInvoiceDocument(r.Context(), invoiceID)
	if err != nil || iv.Content == nil || iv.Status != "issued" || iv.Kind != "invoice" || iv.NeighborID != neighborID || iv.BillingYearID != yearID {
		s.notFound(w, r)
		return
	}
	view, err := s.loadInvoiceDocumentView(r.Context(), iv)
	if err != nil {
		s.invoiceRequestError(w, r, err)
		return
	}
	blob, err := s.documents().invoicePDF(view)
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
	shareID, neighborID, yearID, invoiceID, token, ok := s.resolvePortalShare(w, r)
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
	status := r.FormValue("status")
	if status != "confirmed" && status != "disputed" {
		s.badRequest(w, "Ungültige Rückmeldung.")
		return
	}
	message := strings.TrimSpace(r.FormValue("message"))
	if utf8.RuneCountInString(message) > maxNoteLen {
		s.badRequest(w, "Die Nachricht ist zu lang.")
		return
	}
	if status == "disputed" && message == "" {
		s.badRequest(w, "Bitte den Einwand kurz beschreiben.")
		return
	}
	rawLine := strings.TrimSpace(r.FormValue("line"))
	if utf8.RuneCountInString(rawLine) > maxNameLen {
		s.badRequest(w, "Ungültige Rechnungsposition.")
		return
	}
	iv, err := s.store.GetInvoiceDocument(r.Context(), invoiceID)
	if err != nil || iv.Content == nil || iv.Status != "issued" || iv.Kind != "invoice" || iv.NeighborID != neighborID || iv.BillingYearID != yearID {
		s.notFound(w, r)
		return
	}
	var linePosition *int
	if rawLine != "" {
		idx, err := strconv.Atoi(rawLine)
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

func (s *Server) handleSharedInvoiceEpcQR(w http.ResponseWriter, r *http.Request) {
	_, neighborID, yearID, invoiceID, _, ok := s.resolvePortalShare(w, r)
	if !ok {
		return
	}
	iv, err := s.store.GetInvoiceDocument(r.Context(), invoiceID)
	if err != nil || iv.Status != "issued" || iv.Kind != "invoice" ||
		iv.NeighborID != neighborID || iv.BillingYearID != yearID {
		s.notFound(w, r)
		return
	}
	s.writeInvoiceEpcQR(w, r, iv)
}
