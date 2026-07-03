package server

import (
	"net/http"
	"sort"

	"treckrr/internal/models"
)

// aggRow is one aggregated statistic line (used for KPI lists and bar charts).
type aggRow struct {
	Label string
	Hours float64
	Cost  float64
}

// aggregate groups entries by the key returned from keyFn, summing hours/cost,
// then returns rows sorted by cost descending. Voided entries are skipped.
func aggregate(entries []models.Entry, keyFn func(models.Entry) string) []aggRow {
	order := []string{}
	byKey := map[string]*aggRow{}
	for _, e := range entries {
		if e.Voided {
			continue
		}
		k := keyFn(e)
		row, ok := byKey[k]
		if !ok {
			row = &aggRow{Label: k}
			byKey[k] = row
			order = append(order, k)
		}
		row.Hours += e.Hours
		row.Cost += e.Cost
	}
	out := make([]aggRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost > out[j].Cost })
	return out
}

// maxCost returns the largest cost in the rows (for bar-chart scaling).
func maxCost(rows []aggRow) float64 {
	m := 0.0
	for _, r := range rows {
		if r.Cost > m {
			m = r.Cost
		}
	}
	return m
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	entries, err := s.store.ListEntriesByYear(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}

	// Neighbor id -> name for the by-neighbor aggregation.
	names := map[int64]string{}
	if ns, err := s.store.ListNeighbors(r.Context()); err == nil {
		for _, n := range ns {
			names[n.ID] = n.Name
		}
	}

	var totalCost, totalHours float64
	for _, e := range entries {
		if e.Voided {
			continue
		}
		totalCost += e.Cost
		totalHours += e.Hours
	}

	byNeighbor := aggregate(entries, func(e models.Entry) string { return names[e.NeighborID] })
	byTask := aggregate(entries, func(e models.Entry) string {
		if e.TaskLabel == "" {
			return "Sonstige"
		}
		return e.TaskLabel
	})
	byTractor := aggregate(entries, func(e models.Entry) string { return e.TractorLabel })

	// Payment split.
	payments, _ := s.store.YearPayments(r.Context(), year.ID)
	var paidCost, openCost float64
	members, _ := s.store.ListYearNeighbors(r.Context(), year.ID)
	for _, n := range members {
		c, _, _ := s.store.NeighborTotal(r.Context(), n.ID, year.ID)
		if payments[n.ID] {
			paidCost += c
		} else {
			openCost += c
		}
	}

	data := s.newPage(w, r, "Statistik", "stats")
	if err := s.withYearSelector(r, data, year); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	data["TotalCost"] = totalCost
	data["TotalHours"] = totalHours
	data["PaidCost"] = paidCost
	data["OpenCost"] = openCost
	data["Completed"] = year.Completed()
	data["ByNeighbor"] = byNeighbor
	data["ByNeighborMax"] = maxCost(byNeighbor)
	data["ByTask"] = byTask
	data["ByTaskMax"] = maxCost(byTask)
	data["ByTractor"] = byTractor
	data["ByTractorMax"] = maxCost(byTractor)

	// Year-over-year comparison with the previous billing year.
	if prev, err := s.store.PreviousBillingYear(r.Context(), year.Year); err == nil {
		prevEntries, _ := s.store.ListEntriesByYear(r.Context(), prev.ID)
		var pc, ph float64
		for _, e := range prevEntries {
			if e.Voided {
				continue
			}
			pc += e.Cost
			ph += e.Hours
		}
		data["PrevYear"] = prev.Year
		data["PrevCost"] = pc
		data["PrevHours"] = ph
		data["DiffCost"] = round2(totalCost - pc)
		if pc != 0 {
			data["DiffPct"] = round2((totalCost - pc) / pc * 100)
		}
	}

	s.render(w, r, "stats", data)
}
