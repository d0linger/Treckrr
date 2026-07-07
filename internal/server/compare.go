package server

import (
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
)

func itoa64(n int64) string { return strconv.FormatInt(n, 10) }

func formInt64FromQuery(r *http.Request, name string) int64 {
	v, _ := strconv.ParseInt(r.URL.Query().Get(name), 10, 64)
	return v
}

// diffRow is one comparison line between two bases.
type diffRow struct {
	Label string
	A     decimal.Decimal // value in the selected basis
	B     decimal.Decimal // value in the compared-against basis
	Diff  decimal.Decimal // A - B
	Pct   decimal.Decimal // percentage change vs B
}

func makeDiff(label string, a, b decimal.Decimal) diffRow {
	d := diffRow{Label: label, A: a, B: b, Diff: a.Sub(b).Round(2)}
	if !b.IsZero() {
		d.Pct = a.Sub(b).Div(b).Mul(decimal.NewFromInt(100)).Round(2)
	}
	return d
}

// gespannRates returns name -> hourly rate for all gespanne of a base.
func (s *Server) gespannRates(r *http.Request, baseID int64) map[string]decimal.Decimal {
	out := map[string]decimal.Decimal{}
	gespanne, _ := s.store.ListGespanne(r.Context(), baseID)
	tractors, _ := s.store.ListTractors(r.Context(), baseID)
	loads, _ := s.store.ListLoadLevels(r.Context(), baseID)
	machines, _ := s.store.ListMachines(r.Context(), baseID)
	tById := map[int64]models.Tractor{}
	for _, t := range tractors {
		tById[t.ID] = t
	}
	lById := map[int64]models.LoadLevel{}
	for _, l := range loads {
		lById[l.ID] = l
	}
	mById := map[int64]models.Machine{}
	for _, m := range machines {
		mById[m.ID] = m
	}
	for _, g := range gespanne {
		if g.TractorID == nil || g.LoadLevelID == nil {
			continue
		}
		t, ok1 := tById[*g.TractorID]
		l, ok2 := lById[*g.LoadLevelID]
		if !ok1 || !ok2 {
			continue
		}
		var ms []models.Machine
		for _, mid := range g.MachineIDs {
			if m, ok := mById[mid]; ok {
				ms = append(ms, m)
			}
		}
		out[g.Name] = calc.GespannRate(t, l, ms)
	}
	return out
}

// handlePriceCompare shows the rate differences between two bases so the user
// can preview the impact of a new/changed Bemessungsgrundlage.
func (s *Server) handlePriceCompare(w http.ResponseWriter, r *http.Request) {
	base, ok := s.resolveBase(w, r)
	if !ok {
		return
	}
	bases, err := s.store.ListBases(r.Context())
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}

	data := s.newPage(w, r, "Grundlagen‑Vergleich", "bases")
	data["Base"] = base
	data["Bases"] = bases

	againstID := formInt64FromQuery(r, "against")
	if againstID == 0 || againstID == base.ID {
		// Nothing to compare against yet; show the picker only.
		s.render(w, r, "compare", data)
		return
	}
	against, err := s.store.GetBase(r.Context(), againstID)
	if err != nil {
		s.setFlash(w, r, "error", "Vergleichsgrundlage nicht gefunden.")
		redirect(w, r, "/prices/compare?base="+itoa64(base.ID))
		return
	}
	data["Against"] = against

	// Load levels (cost per PS).
	loadsA, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	loadsB := map[string]decimal.Decimal{}
	if lb, err := s.store.ListLoadLevels(r.Context(), against.ID); err == nil {
		for _, l := range lb {
			loadsB[l.Name] = l.CostPerPS
		}
	}
	var loadDiffs []diffRow
	for _, l := range loadsA {
		loadDiffs = append(loadDiffs, makeDiff(l.Name, l.CostPerPS, loadsB[l.Name]))
	}
	data["LoadDiffs"] = loadDiffs

	// Machines (hourly rate), matched by name.
	machA, _ := s.store.ListMachines(r.Context(), base.ID)
	machB := map[string]decimal.Decimal{}
	if mb, err := s.store.ListMachines(r.Context(), against.ID); err == nil {
		for _, m := range mb {
			machB[m.Name] = calc.MachineRate(m)
		}
	}
	var machDiffs []diffRow
	for _, m := range machA {
		machDiffs = append(machDiffs, makeDiff(m.Name, calc.MachineRate(m), machB[m.Name]))
	}
	data["MachineDiffs"] = machDiffs

	// Gespanne (hourly rate), matched by name.
	gA := s.gespannRates(r, base.ID)
	gB := s.gespannRates(r, against.ID)
	var gDiffs []diffRow
	for name, rate := range gA {
		gDiffs = append(gDiffs, makeDiff(name, rate, gB[name]))
	}
	data["GespannDiffs"] = gDiffs

	s.render(w, r, "compare", data)
}
