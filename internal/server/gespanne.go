package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

// --- audit label resolvers (human names for audit detail) ----------------

func (s *Server) tractorLabel(r *http.Request, id *int64) string {
	if id == nil {
		return "—"
	}
	if t, err := s.store.GetTractor(r.Context(), *id); err == nil {
		return t.Label()
	}
	return "#" + strconv.FormatInt(*id, 10)
}

func (s *Server) loadName(r *http.Request, id *int64) string {
	if id == nil {
		return "—"
	}
	if l, err := s.store.GetLoadLevel(r.Context(), *id); err == nil {
		return l.Name
	}
	return "#" + strconv.FormatInt(*id, 10)
}

func (s *Server) machineNames(r *http.Request, ids []int64) string {
	if len(ids) == 0 {
		return "—"
	}
	ms, err := s.store.MachinesByIDs(r.Context(), ids)
	if err != nil || len(ms) == 0 {
		return "—"
	}
	names := make([]string, 0, len(ms))
	for _, m := range ms {
		names = append(names, m.Name)
	}
	return strings.Join(names, ", ")
}

// partRate is one line of a gespann's cost breakdown.
type partRate struct {
	Label string
	Rate  decimal.Decimal
}

// gespannView pairs a gespann with its resolved parts and hourly rate.
type gespannView struct {
	Gespann   models.Gespann
	Tractor   *models.Tractor
	Load      *models.LoadLevel
	Machines  []models.Machine
	Rate      decimal.Decimal
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
		var newID int64
		newID, err = s.store.CreateGespann(r.Context(), baseID, name, tractorID, loadID, machineIDs, sortOrder)
		if err == nil {
			s.audit(r, "create", "gespann", newID, name)
		}
	} else {
		before, _ := s.store.GetGespann(r.Context(), id)
		err = s.store.UpdateGespann(r.Context(), id, name, tractorID, loadID, machineIDs, sortOrder)
		if err == nil {
			detail := name
			if before != nil {
				if d := diffFields(
					fieldChange{"Name", before.Name, name},
					fieldChange{"Traktor", s.tractorLabel(r, before.TractorID), s.tractorLabel(r, tractorID)},
					fieldChange{"Last", s.loadName(r, before.LoadLevelID), s.loadName(r, loadID)},
					fieldChange{"Maschinen", s.machineNames(r, before.MachineIDs), s.machineNames(r, machineIDs)},
				); d != "" {
					detail = d
				}
			}
			s.audit(r, "update", "gespann", id, detail)
		}
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
	before, _ := s.store.GetGespann(r.Context(), id)
	err = s.store.DeleteGespann(r.Context(), id)
	if err == nil {
		detail := ""
		if before != nil {
			detail = before.Name
		}
		s.audit(r, "delete", "gespann", id, detail)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, gespanneURL(baseID))
}
