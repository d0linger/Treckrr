package server

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/d0linger/treckrr/internal/models"
)

// handleCompany renders the Betriebsdaten (sender/invoice settings) form.
func (s *Server) handleCompany(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Betriebsdaten", "company")
	data["Company"] = c
	s.render(w, r, "company", data)
}

// handleCompanySave persists the Betriebsdaten.
func (s *Server) handleCompanySave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	c := models.Company{
		Name:    trimmed(r, "name"),
		Address: trimmed(r, "address"),
		TaxID:   trimmed(r, "tax_id"),
		TaxNote: trimmed(r, "tax_note"),
		TaxMode: r.FormValue("tax_mode"),
		VATRate: formDecimal(r, "vat_rate"),
		IBAN:    trimmed(r, "iban"),
	}
	switch c.TaxMode {
	case "kleinunternehmer", "pauschal", "regel":
	default:
		c.TaxMode = "pauschal"
	}
	// Zahlungsziel: clamp to a sane 0–365 days; blank/invalid falls back to 14.
	c.PaymentTermDays = 14
	if v, err := strconv.Atoi(strings.TrimSpace(r.FormValue("payment_term_days"))); err == nil && v >= 0 && v <= 365 {
		c.PaymentTermDays = v
	}
	if s.tooLong(w, r, "Name", c.Name, maxNameLen) ||
		s.tooLong(w, r, "Adresse", c.Address, maxNoteLen) ||
		s.tooLong(w, r, "UID/Steuernummer", c.TaxID, maxNameLen) ||
		s.tooLong(w, r, "IBAN", c.IBAN, maxNameLen) ||
		s.tooLong(w, r, "Steuerhinweis", c.TaxNote, maxNoteLen) {
		redirect(w, r, "/admin/company")
		return
	}
	if err := s.store.UpdateCompany(r.Context(), c); err != nil {
		slog.Error("company update failed", "err", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		s.audit(r, "update", "company", 1, "Betriebsdaten aktualisiert")
		s.setFlash(w, r, "success", "Betriebsdaten gespeichert.")
	}
	redirect(w, r, "/admin/company")
}
