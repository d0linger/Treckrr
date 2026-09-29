package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/d0linger/treckrr/internal/einvoice"
)

// handleEInvoiceXML downloads one ebInterface 6.1 document. It accepts only an
// immutable issued invoice snapshot; legacy/live rebuilding is forbidden.
func (s *Server) handleEInvoiceXML(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	iv, err := s.store.GetInvoiceDocument(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	b, err := einvoice.Render(iv)
	if err != nil {
		s.badRequest(w, "E-Rechnung nicht bereit: "+strings.Join(einvoice.MissingFields(iv), ", ")+".")
		return
	}
	name := zipNameSafe.ReplaceAllString(iv.Number, "_") + "-ebinterface-6p1.xml"
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}
