package server

import (
	"net/http"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

// partRate is one line of a gespann's cost breakdown.
type partRate struct {
	Label string
	Rate  float64
}

// gespannView pairs a gespann with its resolved parts and hourly rate.
type gespannView struct {
	Gespann   models.Gespann
	Tractor   *models.Tractor
	Load      *models.LoadLevel
	Machines  []models.Machine
	Rate      float64
	Breakdown []partRate // tractor + each machine, for the full cost breakdown
}

func (s *Server) handleGespanne(w http.ResponseWriter, r *http.Request) {
	base, ok := s.resolveBase(w, r)
	if !ok {
		return
	}
	gespanne, _ := s.store.ListGespanne(r.Context(), base.ID)
	tractors, _ := s.store.ListTractors(r.Context(), base.ID)
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	machines, _ := s.store.ListMachines(r.Context(), base.ID)

	tractorByID := map[int64]models.Tractor{}
	for _, t := range tractors {
		tractorByID[t.ID] = t
	}
	loadByID := map[int64]models.LoadLevel{}
	for _, l := range loads {
		loadByID[l.ID] = l
	}
	machineByID := map[int64]models.Machine{}
	for _, m := range machines {
		machineByID[m.ID] = m
	}

	var views []gespannView
	for _, g := range gespanne {
		v := gespannView{Gespann: g}
		if g.TractorID != nil {
			if t, ok := tractorByID[*g.TractorID]; ok {
				v.Tractor = &t
			}
		}
		if g.LoadLevelID != nil {
			if l, ok := loadByID[*g.LoadLevelID]; ok {
				v.Load = &l
			}
		}
		for _, mid := range g.MachineIDs {
			if m, ok := machineByID[mid]; ok {
				v.Machines = append(v.Machines, m)
			}
		}
		if v.Tractor != nil && v.Load != nil {
			v.Rate = calc.GespannRate(*v.Tractor, *v.Load, v.Machines)
			tr := calc.TractorRate(*v.Tractor, *v.Load)
			v.Breakdown = append(v.Breakdown, partRate{
				Label: v.Tractor.Label() + " · " + v.Load.Name, Rate: tr,
			})
			for _, m := range v.Machines {
				v.Breakdown = append(v.Breakdown, partRate{Label: m.Name, Rate: calc.MachineRate(m)})
			}
		}
		views = append(views, v)
	}

	data := s.newPage(w, r, "Gespanne", "gespanne")
	data["Base"] = base
	data["Views"] = views
	data["Tractors"] = tractors
	data["Loads"] = loads
	data["Machines"] = machines
	data["Locked"] = base.Locked
	s.render(w, r, "gespanne", data)
}

func (s *Server) handleGespannSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, gespanneURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	name := trimmed(r, "name")
	tractorID := formInt64Ptr(r, "tractor_id")
	loadID := formInt64Ptr(r, "load_level_id")
	machineIDs := formMachineIDs(r)
	sortOrder := formInt(r, "sort_order")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		redirect(w, r, gespanneURL(baseID))
		return
	}
	var err error
	if id == 0 {
		_, err = s.store.CreateGespann(r.Context(), baseID, name, tractorID, loadID, machineIDs, sortOrder)
	} else {
		err = s.store.UpdateGespann(r.Context(), id, name, tractorID, loadID, machineIDs, sortOrder)
	}
	if err == nil {
		s.audit(r, "save", "gespann", id, name)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, gespanneURL(baseID))
}

func (s *Server) handleGespannDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, gespanneURL(baseID)) {
		return
	}
	err = s.store.DeleteGespann(r.Context(), id)
	if err == nil {
		s.audit(r, "delete", "gespann", id, "")
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, gespanneURL(baseID))
}
