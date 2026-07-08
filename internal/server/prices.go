package server

import (
	"net/http"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

// tractorView pairs a tractor with its computed rates per load level.
type tractorRateView struct {
	Tractor models.Tractor
	Rates   []loadRate
}

type loadRate struct {
	Load models.LoadLevel
	Rate decimal.Decimal
}

func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request) {
	base, ok := s.resolveBase(w, r)
	if !ok {
		return
	}
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	tractors, _ := s.store.ListTractors(r.Context(), base.ID)
	machines, _ := s.store.ListMachines(r.Context(), base.ID)

	var tractorViews []tractorRateView
	for _, t := range tractors {
		var rates []loadRate
		for _, l := range loads {
			rates = append(rates, loadRate{Load: l, Rate: calc.TractorRate(t, l)})
		}
		tractorViews = append(tractorViews, tractorRateView{Tractor: t, Rates: rates})
	}

	type machineView struct {
		Machine models.Machine
		Rate    decimal.Decimal
	}
	var machineViews []machineView
	for _, m := range machines {
		machineViews = append(machineViews, machineView{Machine: m, Rate: calc.MachineRate(m)})
	}

	cats, _ := s.store.MachineCategories(r.Context(), base.ID)

	data := s.newPage(w, r, "Kosten verwalten", "prices")
	data["Base"] = base
	data["Categories"] = cats
	data["Loads"] = loads
	data["TractorViews"] = tractorViews
	data["MachineViews"] = machineViews
	data["Locked"] = base.Locked
	s.render(w, r, "prices", data)
}

// lockedRedirect reports whether the base is locked; if so it flashes and
// redirects to the given URL and returns true.
func (s *Server) lockedRedirect(w http.ResponseWriter, r *http.Request, baseID int64, target string) bool {
	base, err := s.store.GetBase(r.Context(), baseID)
	if err != nil {
		http.Error(w, "Unbekannte Bemessungsgrundlage", http.StatusBadRequest)
		return true
	}
	if base.Locked {
		s.setFlash(w, r, "error", "Diese Bemessungsgrundlage ist gesperrt und kann nicht geändert werden.")
		redirect(w, r, target)
		return true
	}
	return false
}

// ---- Load levels ---------------------------------------------------------

func (s *Server) handleLoadLevelSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	name := trimmed(r, "name")
	cost := formDecimal(r, "cost_per_ps")
	sort := formInt(r, "sort_order")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	var err error
	action := "update"
	if id == 0 {
		action = "create"
		id, err = s.store.CreateLoadLevel(r.Context(), baseID, name, cost, sort)
	} else {
		err = s.store.UpdateLoadLevel(r.Context(), id, name, cost, sort)
	}
	if err == nil {
		s.audit(r, action, "load_level", id, name)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

func (s *Server) handleLoadLevelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	before, _ := s.store.GetLoadLevel(r.Context(), id)
	err = s.store.DeleteLoadLevel(r.Context(), id)
	if err == nil {
		detail := ""
		if before != nil {
			detail = before.Name
		}
		s.audit(r, "delete", "load_level", id, detail)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// ---- Tractors ------------------------------------------------------------

func (s *Server) handleTractorSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	ident := trimmed(r, "ident")
	name := trimmed(r, "name")
	ps := formDecimal(r, "ps")
	sortOrder := formInt(r, "sort_order")
	if ident == "" || !ps.IsPositive() {
		s.setFlash(w, r, "error", "Bezeichnung und PS (> 0) sind erforderlich.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	var err error
	action := "update"
	if id == 0 {
		action = "create"
		id, err = s.store.CreateTractor(r.Context(), baseID, ident, name, ps, sortOrder)
	} else {
		err = s.store.UpdateTractor(r.Context(), id, ident, name, ps, sortOrder)
	}
	if err == nil {
		s.audit(r, action, "tractor", id, ident)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

func (s *Server) handleTractorToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	active := r.FormValue("active") == "true"
	label := s.tractorLabel(r, &id)
	if err := s.store.SetTractorActive(r.Context(), id, active); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if active {
		s.audit(r, "activate", "tractor", id, label)
		s.setFlash(w, r, "success", "Traktor aktiviert.")
	} else {
		s.audit(r, "deactivate", "tractor", id, label)
		s.setFlash(w, r, "success", "Traktor deaktiviert (bleibt für bestehende Buchungen erhalten).")
	}
	redirect(w, r, pricesURL(baseID))
}

func (s *Server) handleTractorDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	before, _ := s.store.GetTractor(r.Context(), id)
	err = s.store.DeleteTractor(r.Context(), id)
	if err == nil {
		detail := ""
		if before != nil {
			detail = before.Label()
		}
		s.audit(r, "delete", "tractor", id, detail)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// ---- Machines ------------------------------------------------------------

func (s *Server) handleMachineSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	name := trimmed(r, "name")
	width := formDecimal(r, "working_width")
	cost := formDecimal(r, "cost_per_ab")
	category := trimmed(r, "category")
	sortOrder := formInt(r, "sort_order")
	if name == "" || !width.IsPositive() {
		s.setFlash(w, r, "error", "Name und Arbeitsbreite (> 0) sind erforderlich.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	var err error
	action := "update"
	if id == 0 {
		action = "create"
		id, err = s.store.CreateMachine(r.Context(), baseID, name, width, cost, category, sortOrder)
	} else {
		err = s.store.UpdateMachine(r.Context(), id, name, width, cost, category, sortOrder)
	}
	if err == nil {
		s.audit(r, action, "machine", id, name)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

func (s *Server) handleMachineToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	active := r.FormValue("active") == "true"
	name := s.machineNames(r, []int64{id})
	if err := s.store.SetMachineActive(r.Context(), id, active); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if active {
		s.audit(r, "activate", "machine", id, name)
		s.setFlash(w, r, "success", "Maschine aktiviert.")
	} else {
		s.audit(r, "deactivate", "machine", id, name)
		s.setFlash(w, r, "success", "Maschine deaktiviert (bleibt für bestehende Buchungen erhalten).")
	}
	redirect(w, r, pricesURL(baseID))
}

func (s *Server) handleMachineDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	name := ""
	if ms, mErr := s.store.MachinesByIDs(r.Context(), []int64{id}); mErr == nil && len(ms) > 0 {
		name = ms[0].Name
	}
	err = s.store.DeleteMachine(r.Context(), id)
	if err == nil {
		s.audit(r, "delete", "machine", id, name)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// ---- flash helpers -------------------------------------------------------

func (s *Server) flashSaved(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen (evtl. Name bereits vergeben).")
		return
	}
	s.setFlash(w, r, "success", "Gespeichert.")
}

func (s *Server) flashDeleted(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen (evtl. noch in Verwendung).")
		return
	}
	s.setFlash(w, r, "success", "Gelöscht.")
}
