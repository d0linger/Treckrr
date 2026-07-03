package server

import (
	"net/http"
	"strconv"

	"treckrr/internal/models"
)

func (s *Server) handleYears(w http.ResponseWriter, r *http.Request) {
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	bases, err := s.store.ListBases(r.Context())
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
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
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
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

	data := s.newPage(w, r, "Abrechnungsjahre", "years")
	data["Rows"] = rows
	data["Bases"] = bases
	data["HasBases"] = len(bases) > 0
	data["NextYear"] = nextYear
	s.render(w, r, "years", data)
}

func (s *Server) handleYearCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	year := int(formInt64(r, "year"))
	baseID := formInt64(r, "base_id")
	label := trimmed(r, "label")

	if year < 1900 || year > 3000 {
		s.setFlash(w, "error", "Bitte ein gültiges Jahr angeben.")
		redirect(w, r, "/years")
		return
	}
	if baseID == 0 {
		s.setFlash(w, "error", "Bitte eine Bemessungsgrundlage wählen.")
		redirect(w, r, "/years")
		return
	}
	if label == "" {
		label = "Abrechnung " + strconv.Itoa(year)
	}
	id, err := s.store.CreateBillingYear(r.Context(), year, baseID, label)
	if err != nil {
		s.setFlash(w, "error", "Anlegen fehlgeschlagen (Jahr bereits vorhanden?).")
		redirect(w, r, "/years")
		return
	}
	s.audit(r, "create", "year", id, strconv.Itoa(year)+" ("+label+")")
	s.setFlash(w, "success", "Abrechnungsjahr angelegt.")
	redirect(w, r, dashboardURL(id))
}

func (s *Server) handleYearUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := formInt64(r, "base_id")
	label := trimmed(r, "label")

	// Changing the basis is only safe while no entries are booked, otherwise the
	// booked tractor/machine references would point at a different basis.
	count, err := s.store.CountEntriesForYear(r.Context(), id)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		year, err := s.store.GetBillingYear(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if baseID != year.BaseID {
			s.setFlash(w, "error", "Bemessungsgrundlage kann nicht mehr geändert werden – es gibt bereits Buchungen.")
			redirect(w, r, "/years")
			return
		}
	}
	if err := s.store.UpdateBillingYear(r.Context(), id, baseID, label); err != nil {
		s.setFlash(w, "error", "Aktualisierung fehlgeschlagen.")
	} else {
		s.audit(r, "update", "year", id, label)
		s.setFlash(w, "success", "Abrechnungsjahr aktualisiert.")
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	status := models.YearInProgress
	if r.FormValue("status") == models.YearCompleted {
		status = models.YearCompleted
	}
	if err := s.store.SetYearStatus(r.Context(), id, status); err != nil {
		s.setFlash(w, "error", "Statuswechsel fehlgeschlagen.")
	} else if status == models.YearCompleted {
		// Every neighbor starts as "open" when the year is closed for billing.
		if err := s.store.ResetYearPayments(r.Context(), id); err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		s.audit(r, "complete", "year", id, "")
		s.setFlash(w, "success", "Abrechnungsjahr abgeschlossen. Zahlungsstatus je Nachbar steht auf offen.")
	} else {
		s.audit(r, "reopen", "year", id, "")
		s.setFlash(w, "success", "Abrechnungsjahr wieder geöffnet.")
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
	count, err := s.store.CountEntriesForYear(r.Context(), id)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if count > 0 {
		s.setFlash(w, "error", "Jahr enthält Buchungen und kann nicht gelöscht werden.")
		redirect(w, r, "/years")
		return
	}
	if err := s.store.DeleteBillingYear(r.Context(), id); err != nil {
		s.setFlash(w, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "delete", "year", id, "")
		s.setFlash(w, "success", "Abrechnungsjahr gelöscht.")
	}
	redirect(w, r, "/years")
}
