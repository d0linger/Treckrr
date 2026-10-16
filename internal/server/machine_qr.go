package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// QR labels on machines, tractors and rigs open a read-only scan page. Choosing
// a neighbor opens the existing booking form as a prefilled draft; no scan
// route ever writes.

// scanTarget is one scannable piece of equipment.
type scanTarget struct {
	Kind      string // machine, tractor, gespann
	KindLabel string // Maschine, Traktor, Gespann
	ID        int64
	BaseID    int64
	Name      string
	Detail    string
	Active    bool
}

// BookPath is the scan landing URL encoded in the QR code.
func (t scanTarget) BookPath() string {
	switch t.Kind {
	case "tractor":
		return "/tractors/" + itoa64(t.ID) + "/book"
	case "gespann":
		return "/gespanne/" + itoa64(t.ID) + "/book"
	}
	return "/machines/" + itoa64(t.ID) + "/book"
}

// QRPath is the label image URL.
func (t scanTarget) QRPath() string {
	switch t.Kind {
	case "tractor":
		return "/prices/tractors/" + itoa64(t.ID) + "/qr.png"
	case "gespann":
		return "/gespanne/" + itoa64(t.ID) + "/qr.png"
	}
	return "/prices/machines/" + itoa64(t.ID) + "/qr.png"
}

// BackURL leads back to where the item is maintained.
func (t scanTarget) BackURL() string {
	if t.Kind == "gespann" {
		return "/gespanne?base=" + itoa64(t.BaseID)
	}
	return "/prices?base=" + itoa64(t.BaseID) + "#" + t.Kind + "-" + itoa64(t.ID)
}

func machineTarget(m models.Machine) scanTarget {
	detail := web.Num(m.WorkingWidth) + " m · " + web.Money(m.HourlyRate()) + "/h"
	if m.Category != "" {
		detail = m.Category + " · " + detail
	}
	return scanTarget{Kind: "machine", KindLabel: "Maschine", ID: m.ID, BaseID: m.BaseID, Name: m.Name, Detail: detail, Active: m.Active}
}

func tractorTarget(t models.Tractor) scanTarget {
	return scanTarget{Kind: "tractor", KindLabel: "Traktor", ID: t.ID, BaseID: t.BaseID, Name: t.Label(),
		Detail: web.Num(t.PS) + " PS", Active: t.Active}
}

// loadScanTarget resolves the kind and path id; ok=false means a 404 or 500
// was written.
func (s *Server) loadScanTarget(w http.ResponseWriter, r *http.Request, kind string) (scanTarget, bool) {
	id, err := pathID(r)
	if err != nil || id <= 0 {
		s.notFound(w, r)
		return scanTarget{}, false
	}
	switch kind {
	case "machine":
		machines, err := s.store.MachinesByIDs(r.Context(), []int64{id})
		if err != nil {
			s.serverError(w, "scan: load machine", err)
			return scanTarget{}, false
		}
		if len(machines) != 1 {
			s.notFound(w, r)
			return scanTarget{}, false
		}
		return machineTarget(machines[0]), true
	case "tractor":
		tractor, err := s.store.GetTractor(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return scanTarget{}, false
		}
		if err != nil {
			s.serverError(w, "scan: load tractor", err)
			return scanTarget{}, false
		}
		return tractorTarget(*tractor), true
	default:
		g, err := s.store.GetGespann(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return scanTarget{}, false
		}
		if err != nil {
			s.serverError(w, "scan: load rig", err)
			return scanTarget{}, false
		}
		return s.gespannTarget(r, *g), true
	}
}

// gespannTarget names the rig's parts; a missing part only shortens the detail.
func (s *Server) gespannTarget(r *http.Request, g models.Gespann) scanTarget {
	var parts []string
	if g.TractorID != nil {
		if t, err := s.store.GetTractor(r.Context(), *g.TractorID); err == nil {
			parts = append(parts, t.Label())
		}
	}
	if len(g.MachineIDs) > 0 {
		if machines, err := s.store.MachinesByIDs(r.Context(), g.MachineIDs); err == nil {
			for _, m := range machines {
				parts = append(parts, m.Name)
			}
		}
	}
	return scanTarget{Kind: "gespann", KindLabel: "Gespann", ID: g.ID, BaseID: g.BaseID, Name: g.Name,
		Detail: strings.Join(parts, " + "), Active: true}
}

// renderScan shows the scan landing: open years of the item's basis and their
// active neighbors, each linking to a prefilled booking draft.
func (s *Server) renderScan(w http.ResponseWriter, r *http.Request, target scanTarget) {
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		s.serverError(w, "scan: years", err)
		return
	}
	eligible := years[:0]
	for _, year := range years {
		if year.BaseID == target.BaseID && !year.Completed() {
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
	if selected != nil && target.Active {
		neighbors, err = s.store.ListYearNeighbors(r.Context(), selected.ID)
		if err != nil {
			s.serverError(w, "scan: neighbors", err)
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
	data := s.newPage(w, r, target.KindLabel+" buchen", "prices")
	data["Scan"] = target
	data["MachineYears"] = eligible
	data["SelectedYear"] = selected
	data["Neighbors"] = neighbors
	s.render(w, r, "machine_book", data)
}

func (s *Server) handleMachineBook(w http.ResponseWriter, r *http.Request) {
	if target, ok := s.loadScanTarget(w, r, "machine"); ok {
		s.renderScan(w, r, target)
	}
}

func (s *Server) handleTractorBook(w http.ResponseWriter, r *http.Request) {
	if target, ok := s.loadScanTarget(w, r, "tractor"); ok {
		s.renderScan(w, r, target)
	}
}

func (s *Server) handleGespannBook(w http.ResponseWriter, r *http.Request) {
	if target, ok := s.loadScanTarget(w, r, "gespann"); ok {
		s.renderScan(w, r, target)
	}
}

// writeScanQR renders the authenticated deep link of one item as a PNG.
func (s *Server) writeScanQR(w http.ResponseWriter, r *http.Request, kind string) {
	target, ok := s.loadScanTarget(w, r, kind)
	if !ok {
		return
	}
	png, err := qrPNG(s.absoluteURL(r, target.BookPath()))
	if err != nil {
		s.serverError(w, "scan qr", err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(png)
}

func (s *Server) handleMachineQR(w http.ResponseWriter, r *http.Request) {
	s.writeScanQR(w, r, "machine")
}

func (s *Server) handleTractorQR(w http.ResponseWriter, r *http.Request) {
	s.writeScanQR(w, r, "tractor")
}

func (s *Server) handleGespannQR(w http.ResponseWriter, r *http.Request) {
	s.writeScanQR(w, r, "gespann")
}

// handleMachineLabels renders a printable sheet of every rig, active tractor
// and active machine of a price basis. Existing inactive labels still resolve
// to a warning.
func (s *Server) handleMachineLabels(w http.ResponseWriter, r *http.Request) {
	base, ok := s.resolveBase(w, r)
	if !ok {
		return
	}
	machines, err := s.store.ListActiveMachines(r.Context(), base.ID)
	if err != nil {
		s.serverError(w, "labels: machines", err)
		return
	}
	tractors, err := s.store.ListActiveTractors(r.Context(), base.ID)
	if err != nil {
		s.serverError(w, "labels: tractors", err)
		return
	}
	gespanne, err := s.store.ListGespanne(r.Context(), base.ID)
	if err != nil {
		s.serverError(w, "labels: rigs", err)
		return
	}
	targets := make([]scanTarget, 0, len(machines)+len(tractors)+len(gespanne))
	for _, g := range gespanne {
		targets = append(targets, s.gespannTarget(r, g))
	}
	for _, t := range tractors {
		targets = append(targets, tractorTarget(t))
	}
	for _, m := range machines {
		targets = append(targets, machineTarget(m))
	}
	data := s.newPage(w, r, "QR-Etiketten", "prices")
	data["Base"] = base
	data["Targets"] = targets
	s.render(w, r, "machine_labels", data)
}
