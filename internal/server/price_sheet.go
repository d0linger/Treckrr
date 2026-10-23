package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
)

// handlePriceSheet renders the neighbor-facing price list of one basis as a
// PDF: tractors by load level, active machines, rigs and travel costs. It
// contains rates only, so every signed-in role may download it.
func (s *Server) handlePriceSheet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	base, err := s.store.GetBase(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	s.writePriceSheet(w, r, base)
}

// handleSharedPriceSheet serves the price list of the year behind a running
// statement link, so a neighbor can check the rates the open lines use.
func (s *Server) handleSharedPriceSheet(w http.ResponseWriter, r *http.Request) {
	_, _, yearID, _, ok := s.resolveStatementShare(w, r)
	if !ok {
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil || year.Base == nil {
		s.notFound(w, r)
		return
	}
	s.writePriceSheet(w, r, year.Base)
}

// writePriceSheet builds and sends the price list of one basis.
func (s *Server) writePriceSheet(w http.ResponseWriter, r *http.Request, base *models.PriceBase) {
	id := base.ID
	loads, err := s.store.ListLoadLevels(r.Context(), id)
	if err != nil {
		s.serverError(w, "price sheet: loads", err)
		return
	}
	allTractors, err := s.store.ListTractors(r.Context(), id)
	if err != nil {
		s.serverError(w, "price sheet: tractors", err)
		return
	}
	allMachines, err := s.store.ListMachines(r.Context(), id)
	if err != nil {
		s.serverError(w, "price sheet: machines", err)
		return
	}
	rigs, err := s.store.ListGespanne(r.Context(), id)
	if err != nil {
		s.serverError(w, "price sheet: rigs", err)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, "price sheet: company", err)
		return
	}

	sheet := pdf.PriceSheet{
		IssuerName: company.Name, IssuerAddress: company.Address, BaseName: base.Name, ValidFrom: base.Year,
		Today: time.Now(), TravelFlat: company.TravelFlat, TravelPerKm: company.TravelPerKm,
	}
	for _, l := range loads {
		sheet.Loads = append(sheet.Loads, pdf.PriceSheetLoad{Name: l.Name, CostPerPS: l.CostPerPS})
	}
	tractorByID := map[int64]models.Tractor{}
	for _, t := range allTractors {
		tractorByID[t.ID] = t
		if !t.Active {
			continue
		}
		row := pdf.PriceSheetTractor{Label: t.Label(), PS: t.PS}
		for _, l := range loads {
			row.Rates = append(row.Rates, calc.TractorRate(t, l))
		}
		sheet.Tractors = append(sheet.Tractors, row)
	}
	machineByID := map[int64]models.Machine{}
	for _, m := range allMachines {
		machineByID[m.ID] = m
		if m.Active {
			sheet.Machines = append(sheet.Machines, pdf.PriceSheetMachine{Name: m.Name, Category: m.Category,
				Width: m.WorkingWidth, CostPerAB: m.CostPerAB, Rate: calc.MachineRate(m)})
		}
	}
	loadByID := map[int64]models.LoadLevel{}
	for _, l := range loads {
		loadByID[l.ID] = l
	}
	for _, g := range rigs {
		var tractor *models.Tractor
		var load *models.LoadLevel
		var parts []string
		if g.TractorID != nil {
			if t, ok := tractorByID[*g.TractorID]; ok {
				tractor = &t
				parts = append(parts, t.Label())
			}
		}
		if g.LoadLevelID != nil {
			if l, ok := loadByID[*g.LoadLevelID]; ok {
				load = &l
				parts = append(parts, l.Name)
			}
		}
		var machines []models.Machine
		for _, mid := range g.MachineIDs {
			if m, ok := machineByID[mid]; ok {
				machines = append(machines, m)
				parts = append(parts, m.Name)
			}
		}
		sheet.Rigs = append(sheet.Rigs, pdf.PriceSheetRig{Name: g.Name, Parts: strings.Join(parts, " · "),
			Rate: calc.GespannRate(tractor, load, machines)})
	}
	if (company.TaxMode == "pauschal" || company.TaxMode == "regel") && company.VATRate.IsPositive() {
		sheet.TaxNote = "Alle Sätze netto, zuzüglich " + strings.Replace(company.VATRate.String(), ".", ",", 1) + " % Umsatzsteuer."
	} else {
		sheet.TaxNote = company.TaxNote
	}

	blob, err := pdf.RenderPriceSheet(sheet)
	if err != nil {
		s.serverError(w, "price sheet: render", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="Preisblatt_`+strconv.Itoa(base.Year)+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}
