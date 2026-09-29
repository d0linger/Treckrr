package server

import (
	"net/http"
	"strconv"

	"github.com/d0linger/treckrr/internal/models"
)

// machineByPath resolves the base-specific machine encoded in a QR route.
func (s *Server) machineByPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return 0, false
	}
	return id, true
}

// handleMachineBook renders a read-only scan landing page. Choosing a neighbor
// opens the existing booking form as a prefilled draft; this route never writes.
func (s *Server) handleMachineBook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.machineByPath(w, r)
	if !ok {
		return
	}
	machines, err := s.store.MachinesByIDs(r.Context(), []int64{id})
	if err != nil {
		s.serverError(w, "machine scan: load machine", err)
		return
	}
	if len(machines) != 1 {
		s.notFound(w, r)
		return
	}
	machine := machines[0]
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		s.serverError(w, "machine scan: years", err)
		return
	}
	eligible := years[:0]
	for _, year := range years {
		if year.BaseID == machine.BaseID && !year.Completed() {
			eligible = append(eligible, year)
		}
	}
	var selectedID int64
	if raw := r.URL.Query().Get("year"); raw != "" {
		selectedID, _ = strconv.ParseInt(raw, 10, 64)
	}
	var selected *models.BillingYear
	for i := range eligible {
		if selectedID == 0 || eligible[i].ID == selectedID {
			selected = &eligible[i]
			break
		}
	}
	if selected == nil && len(eligible) > 0 {
		selected = &eligible[0]
	}
	var neighbors []models.Neighbor
	if selected != nil && machine.Active {
		neighbors, err = s.store.ListYearNeighbors(r.Context(), selected.ID)
		if err != nil {
			s.serverError(w, "machine scan: neighbors", err)
			return
		}
		active := neighbors[:0]
		for _, neighbor := range neighbors {
			if !neighbor.Archived {
				active = append(active, neighbor)
			}
		}
		neighbors = active
	}
	data := s.newPage(w, r, "Maschine buchen", "prices")
	data["Machine"] = machine
	data["MachineYears"] = eligible
	data["SelectedYear"] = selected
	data["Neighbors"] = neighbors
	s.render(w, r, "machine_book", data)
}

// handleMachineQR renders a stable authenticated deep link for one machine.
func (s *Server) handleMachineQR(w http.ResponseWriter, r *http.Request) {
	id, ok := s.machineByPath(w, r)
	if !ok {
		return
	}
	machines, err := s.store.MachinesByIDs(r.Context(), []int64{id})
	if err != nil {
		s.serverError(w, "machine qr: load machine", err)
		return
	}
	if len(machines) != 1 {
		s.notFound(w, r)
		return
	}
	png, err := qrPNG(s.absoluteURL(r, "/machines/"+strconv.FormatInt(id, 10)+"/book"))
	if err != nil {
		s.serverError(w, "machine qr", err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}

// handleMachineLabels renders a printable sheet for every active machine in a
// selected price basis. Existing inactive labels still resolve to a warning.
func (s *Server) handleMachineLabels(w http.ResponseWriter, r *http.Request) {
	base, ok := s.resolveBase(w, r)
	if !ok {
		return
	}
	machines, err := s.store.ListActiveMachines(r.Context(), base.ID)
	if err != nil {
		s.serverError(w, "machine labels", err)
		return
	}
	data := s.newPage(w, r, "Maschinen-Etiketten", "prices")
	data["Base"] = base
	data["Machines"] = machines
	s.render(w, r, "machine_labels", data)
}
