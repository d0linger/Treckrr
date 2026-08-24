package server

import (
	"net/http"
	"strconv"

	"github.com/d0linger/treckrr/internal/models"
)

func (s *Server) handleYears(w http.ResponseWriter, r *http.Request) {
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	bases, err := s.store.ListBases(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	type row struct {
		ID        int64
		Year      int
		Label     string
		BaseID    int64
		Base      string
		Entries   int
		Completed bool
	}
	var rows []row
	for _, y := range years {
		count, err := s.store.CountEntriesForYear(r.Context(), y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		baseName := ""
		if y.Base != nil {
			baseName = strconv.Itoa(y.Base.Year) + " — " + y.Base.Name
		}
		rows = append(rows, row{
			ID: y.ID, Year: y.Year, Label: y.Label,
			BaseID: y.BaseID, Base: baseName, Entries: count, Completed: y.Completed(),
		})
	}

	nextYear := 0
	if len(years) > 0 {
		nextYear = years[0].Year + 1 // list is ordered newest first
	}

	// First-run guidance: show a "getting started" checklist until the four
	// setup steps (basis, year, neighbor, first booking) are all done.
	hasEntries := false
	for _, rw := range rows {
		if rw.Entries > 0 {
			hasEntries = true
			break
		}
	}
	hasNeighbors, err := s.store.AnyNeighbors(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	data := s.newPage(w, r, "Abrechnungsjahre", "years")
	data["Rows"] = rows
	data["Bases"] = bases
	data["HasBases"] = len(bases) > 0
	data["HasYears"] = len(rows) > 0
	data["HasNeighbors"] = hasNeighbors
	data["HasEntries"] = hasEntries
	setupComplete := len(bases) > 0 && len(rows) > 0 && hasNeighbors && hasEntries
	data["Onboarding"] = !setupComplete
	data["NextYear"] = nextYear
	s.render(w, r, "years", data)
}

func (s *Server) handleYearCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	year := formInt(r, "year")
	baseID := formInt64(r, "base_id")
	label := trimmed(r, "label")

	if year < 1900 || year > 3000 {
		s.setFlash(w, r, "error", "Bitte ein gültiges Jahr angeben.")
		redirect(w, r, "/years")
		return
	}
	if baseID == 0 {
		s.setFlash(w, r, "error", "Bitte eine Bemessungsgrundlage wählen.")
		redirect(w, r, "/years")
		return
	}
	if s.tooLong(w, r, "Bezeichnung", label, maxNameLen) {
		redirect(w, r, "/years")
		return
	}
	if label == "" {
		label = "Abrechnung " + strconv.Itoa(year)
	}
	id, err := s.store.CreateBillingYear(r.Context(), year, baseID, label)
	if err != nil {
		s.setFlash(w, r, "error", "Anlegen fehlgeschlagen (Jahr bereits vorhanden?).")
		redirect(w, r, "/years")
		return
	}
	s.audit(r, "create", "year", id, strconv.Itoa(year)+" ("+label+")")
	s.setFlash(w, r, "success", "Abrechnungsjahr angelegt.")
	redirect(w, r, dashboardURL(id))
}

func (s *Server) handleYearUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	baseID := formInt64(r, "base_id")
	label := trimmed(r, "label")
	if s.tooLong(w, r, "Bezeichnung", label, maxNameLen) {
		redirect(w, r, "/years")
		return
	}

	// Load the year once, up front — used for the basis-change safety check AND the
	// audit diff (GetBillingYear returns nil on error and also populates .Base). A
	// missing/unreadable year from the URL path is a 404, as before.
	before, err := s.store.GetBillingYear(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Changing the basis is only safe while no entries are booked, otherwise the
	// booked tractor/machine references would point at a different basis.
	count, err := s.store.CountEntriesForYear(r.Context(), id)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if count > 0 && baseID != before.BaseID {
		s.setFlash(w, r, "error", "Bemessungsgrundlage kann nicht mehr geändert werden – es gibt bereits Buchungen.")
		redirect(w, r, "/years")
		return
	}

	if err := s.store.UpdateBillingYear(r.Context(), id, baseID, label); err != nil {
		s.setFlash(w, r, "error", "Aktualisierung fehlgeschlagen.")
	} else {
		// A basis switch is the most consequential change here (it shifts the whole
		// year's price basis), so decide it on the id — base names are NOT unique, so
		// comparing names would drop a real switch between two like-named bases. The
		// old name is already loaded on before.Base (no extra query); resolve only the
		// new one. If the two names happen to collide, emit an explicit marker so the
		// switch is never swallowed by the value-comparing diff.
		grundlage := ""
		if baseID != before.BaseID {
			oldName := s.baseName(r, before.BaseID)
			if before.Base != nil {
				oldName = before.Base.Name
			}
			newName := s.baseName(r, baseID)
			if oldName == newName {
				grundlage = "Grundlage: geändert (" + newName + ")"
			} else {
				grundlage = "Grundlage: " + orDash(oldName) + " → " + orDash(newName)
			}
		}
		detail := label
		d := diffFields(fieldChange{"Bezeichnung", before.Label, label})
		if grundlage != "" {
			if d == "" {
				d = grundlage
			} else {
				d += " · " + grundlage
			}
		}
		if d != "" {
			detail = d
		}
		s.audit(r, "update", "year", id, detail)
		s.setFlash(w, r, "success", "Abrechnungsjahr aktualisiert.")
	}
	redirect(w, r, "/years")
}

// handleYearStatus toggles a billing year between "in progress" and "completed".
func (s *Server) handleYearStatus(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	status := models.YearInProgress
	if r.FormValue("status") == models.YearCompleted {
		status = models.YearCompleted
	}
	if err := s.store.SetYearStatus(r.Context(), id, status); err != nil {
		s.setFlash(w, r, "error", "Statuswechsel fehlgeschlagen.")
	} else if status == models.YearCompleted {
		// Every neighbor starts as "open" when the year is closed for billing.
		if err := s.store.ResetYearPayments(r.Context(), id); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		s.audit(r, "complete", "year", id, "Jahr "+s.yearLabel(r, id))
		s.setFlash(w, r, "success", "Abrechnungsjahr abgeschlossen. Zahlungsstatus je Nachbar steht auf offen.")
	} else {
		s.audit(r, "reopen", "year", id, "Jahr "+s.yearLabel(r, id))
		s.setFlash(w, r, "success", "Abrechnungsjahr wieder geöffnet.")
	}
	if r.FormValue("origin") == "dashboard" {
		redirect(w, r, dashboardURL(id))
		return
	}
	redirect(w, r, "/years")
}

func (s *Server) handleYearDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Same cascade as the neighbour delete: billing_years is the FK parent of
	// entries, payments, neighbor_ledger and invoices, so bookings alone are not a
	// sufficient guard — a year holding only carry-forwards or payments would be
	// erased with them.
	blockers, err := s.store.YearDeleteBlockers(r.Context(), id)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if blockers.Any() {
		s.setFlash(w, r, "error", "Jahr enthält "+describeDeleteBlockers(blockers)+
			" und kann nicht gelöscht werden.")
		redirect(w, r, "/years")
		return
	}
	label := s.yearLabel(r, id) // resolve before the row is gone
	if err := s.store.DeleteBillingYear(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "delete", "year", id, "Jahr "+label)
		s.setFlash(w, r, "success", "Abrechnungsjahr gelöscht.")
	}
	redirect(w, r, "/years")
}
