package server

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// aggRow is one aggregated statistic line (used for KPI lists and bar charts).
type aggRow struct {
	Label string
	Hours decimal.Decimal
	Cost  decimal.Decimal
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
		row.Hours = row.Hours.Add(e.Hours)
		row.Cost = row.Cost.Add(e.Cost)
	}
	out := make([]aggRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Cost.GreaterThan(out[j].Cost) })
	return out
}

// maxCost returns the largest cost in the rows (for bar-chart scaling).
func maxCost(rows []aggRow) decimal.Decimal {
	m := decimal.Zero
	for _, r := range rows {
		if r.Cost.GreaterThan(m) {
			m = r.Cost
		}
	}
	return m
}

// yearStat is one row of the all-years overview.
type yearStat struct {
	Year      int
	YearID    int64
	Cost      decimal.Decimal // Leistungen (bookings)
	Hours     decimal.Decimal
	Ledger    decimal.Decimal // signed ledger sum (verrechnung)
	Net       decimal.Decimal // Cost + Ledger
	PaidCost  decimal.Decimal
	OpenCost  decimal.Decimal
	Completed bool
}

// handleStatsAll renders a cross-year overview: per-year revenue, hours and
// paid/open split, a revenue-per-year bar chart, and grand totals.
func (s *Server) handleStatsAll(w http.ResponseWriter, r *http.Request) {
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}

	stats := make([]yearStat, 0, len(years))
	revenue := make([]aggRow, 0, len(years))
	var grandCost, grandHours, grandLedger, grandPaid, grandOpen decimal.Decimal
	for _, y := range years {
		entries, err := s.store.ListEntriesByYear(r.Context(), y.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		var cost, hours decimal.Decimal
		for _, e := range entries {
			if e.Voided {
				continue
			}
			cost = cost.Add(e.Cost)
			hours = hours.Add(e.Hours)
		}
		paid, open, err := s.store.YearPaymentTotals(r.Context(), y.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		led, err := s.store.YearLedgerSum(r.Context(), y.ID)
		if err != nil {
			http.Error(w, "Interner Fehler", http.StatusInternalServerError)
			return
		}
		stats = append(stats, yearStat{
			Year: y.Year, YearID: y.ID, Cost: cost, Hours: hours,
			Ledger: led, Net: cost.Add(led),
			PaidCost: paid, OpenCost: open, Completed: y.Completed(),
		})
		revenue = append(revenue, aggRow{Label: strconv.Itoa(y.Year), Hours: hours, Cost: cost})
		grandCost = grandCost.Add(cost)
		grandHours = grandHours.Add(hours)
		grandLedger = grandLedger.Add(led)
		grandPaid = grandPaid.Add(paid)
		grandOpen = grandOpen.Add(open)
	}

	data := s.newPage(w, r, "Statistik – Alle Jahre", "stats")
	data["Stats"] = stats
	data["Revenue"] = revenue
	data["RevenueMax"] = maxCost(revenue)
	data["GrandCost"] = grandCost
	data["GrandHours"] = grandHours
	data["GrandLedger"] = grandLedger
	data["GrandNet"] = grandCost.Add(grandLedger)
	data["HasLedger"] = !grandLedger.IsZero()
	data["GrandPaid"] = grandPaid
	data["GrandOpen"] = grandOpen
	s.render(w, r, "stats_all", data)
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

	var totalCost, totalHours decimal.Decimal
	for _, e := range entries {
		if e.Voided {
			continue
		}
		totalCost = totalCost.Add(e.Cost)
		totalHours = totalHours.Add(e.Hours)
	}

	byNeighbor := aggregate(entries, func(e models.Entry) string { return names[e.NeighborID] })
	byTask := aggregate(entries, func(e models.Entry) string {
		if e.TaskLabel == "" {
			return "Sonstige"
		}
		return e.TaskLabel
	})
	byTractor := aggregate(entries, func(e models.Entry) string { return e.TractorLabel })

	// Payment split (paid vs open), computed in a single query.
	paidCost, openCost, err := s.store.YearPaymentTotals(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	// Net result = bookings + signed ledger (credits I owe reduce it), so the
	// bottom line isn't overstated by counting only income.
	ledgerSum, err := s.store.YearLedgerSum(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
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
	data["LedgerSum"] = ledgerSum
	data["NetResult"] = totalCost.Add(ledgerSum)
	data["HasLedger"] = !ledgerSum.IsZero()
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
		var pc, ph decimal.Decimal
		for _, e := range prevEntries {
			if e.Voided {
				continue
			}
			pc = pc.Add(e.Cost)
			ph = ph.Add(e.Hours)
		}
		diff := totalCost.Sub(pc).Round(2)
		data["PrevYear"] = prev.Year
		data["PrevCost"] = pc
		data["PrevHours"] = ph
		data["DiffCost"] = diff
		// Sign as booleans: templates must not compare a decimal to a float.
		data["DiffUp"] = diff.IsPositive()
		data["DiffDown"] = diff.IsNegative()
		if !pc.IsZero() {
			pct := totalCost.Sub(pc).Div(pc).Mul(decimal.NewFromInt(100)).Round(2)
			data["DiffPct"] = pct
			data["DiffPctUp"] = pct.IsPositive()
		}
	}

	s.render(w, r, "stats", data)
}
