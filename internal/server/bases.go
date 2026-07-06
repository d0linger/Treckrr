package server

import (
	"net/http"
	"strconv"

	"treckrr/internal/models"
)

// baseView pairs a basis with whether a billing year references it.
type baseView struct {
	Base  models.PriceBase
	InUse bool
}

func (s *Server) handleBases(w http.ResponseWriter, r *http.Request) {
	bases, err := s.store.ListBases(r.Context())
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	views := make([]baseView, 0, len(bases))
	for _, b := range bases {
		inUse, err := s.store.BaseInUse(r.Context(), b.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		views = append(views, baseView{Base: b, InUse: inUse})
	}

	data := s.newPage(w, r, "Bemessungsgrundlagen", "bases")
	data["Bases"] = bases
	data["BaseViews"] = views
	// Suggest the next year based on the latest existing basis.
	nextYear := 0
	if len(bases) > 0 {
		nextYear = bases[0].Year + 1
	}
	data["NextYear"] = nextYear
	s.render(w, r, "bases", data)
}

// handleBaseCreate creates a new pricing basis. If a source basis is selected,
// all its values are cloned; otherwise an empty basis is created.
func (s *Server) handleBaseCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	year := formInt(r, "year")
	name := trimmed(r, "name")
	if year < 1900 || year > 3000 {
		s.setFlash(w, r, "error", "Bitte ein gültiges Jahr angeben.")
		redirect(w, r, "/bases")
		return
	}
	if name == "" {
		name = "Bemessungsgrundlage " + strconv.Itoa(year)
	}

	var (
		newID int64
		err   error
	)
	if srcID := formInt64(r, "source_base_id"); srcID != 0 {
		newID, err = s.store.CloneBase(r.Context(), srcID, year, name)
	} else {
		newID, err = s.store.CreateEmptyBase(r.Context(), year, name)
	}
	if err != nil {
		s.setFlash(w, r, "error", "Anlegen fehlgeschlagen (Jahr bereits vorhanden?).")
	} else {
		s.audit(r, "create", "base", newID, name)
		s.setFlash(w, r, "success", "Bemessungsgrundlage angelegt. Vorjahreswerte bleiben unverändert.")
	}
	redirect(w, r, "/bases")
}

// handleBaseUpdate renames a basis and updates its "valid from" year.
func (s *Server) handleBaseUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	name := trimmed(r, "name")
	year := formInt(r, "year")
	if name == "" || year < 1900 || year > 3000 {
		s.setFlash(w, r, "error", "Bitte Name und gültiges Jahr angeben.")
		redirect(w, r, "/bases")
		return
	}
	if err := s.store.UpdateBase(r.Context(), id, year, name); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen (Jahr bereits vergeben?).")
	} else {
		s.audit(r, "update", "base", id, name)
		s.setFlash(w, r, "success", "Bemessungsgrundlage aktualisiert.")
	}
	redirect(w, r, "/bases")
}

// handleBaseDelete removes a basis, but only when no billing year uses it.
func (s *Server) handleBaseDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	inUse, err := s.store.BaseInUse(r.Context(), id)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	if inUse {
		s.setFlash(w, r, "error", "Grundlage wird von mindestens einem Abrechnungsjahr verwendet und kann nicht gelöscht werden.")
		redirect(w, r, "/bases")
		return
	}
	if err := s.store.DeleteBase(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "delete", "base", id, "")
		s.setFlash(w, r, "success", "Bemessungsgrundlage gelöscht.")
	}
	redirect(w, r, "/bases")
}

func (s *Server) handleBaseLock(w http.ResponseWriter, r *http.Request) {
	s.setBaseLock(w, r, true)
}

func (s *Server) handleBaseUnlock(w http.ResponseWriter, r *http.Request) {
	s.setBaseLock(w, r, false)
}

func (s *Server) setBaseLock(w http.ResponseWriter, r *http.Request, locked bool) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.store.SetBaseLocked(r.Context(), id, locked); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if locked {
		s.audit(r, "lock", "base", id, "")
		s.setFlash(w, r, "success", "Bemessungsgrundlage gesperrt.")
	} else {
		s.audit(r, "unlock", "base", id, "")
		s.setFlash(w, r, "success", "Bemessungsgrundlage entsperrt.")
	}
	redirect(w, r, "/bases")
}
