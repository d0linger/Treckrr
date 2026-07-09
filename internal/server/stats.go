package server

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// sparkView holds SVG polygon/polyline point strings for a tile's trend
// sparkline (viewBox 0 0 100 32). Points ride on SVG attributes — CSP-safe.
type sparkView struct {
	Line string // polyline points
	Area string // polygon points (line + baseline corners)
}

// makeSpark normalises a value series into an SVG sparkline. Returns nil for
// fewer than two points (nothing to trend).
func makeSpark(vals []decimal.Decimal) *sparkView {
	n := len(vals)
	if n < 2 {
		return nil
	}
	fs := make([]float64, n)
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, v := range vals {
		f, _ := v.Float64()
		fs[i] = f
		lo, hi = math.Min(lo, f), math.Max(hi, f)
	}
	rng := hi - lo
	if rng == 0 {
		rng = 1
	}
	var b strings.Builder
	for i, f := range fs {
		x := float64(i) / float64(n-1) * 100
		y := 29 - (f-lo)/rng*27 // baseline padding: y in ~2..29
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
	}
	line := b.String()
	return &sparkView{Line: line, Area: line + " 100.0,32 0.0,32"}
}

// aggRow is one aggregated statistic line (used for KPI lists and bar charts).
type aggRow struct {
	Label string
	Hours decimal.Decimal
	Cost  decimal.Decimal
}

// ledgerBar is one bar of the per-neighbor verrechnung chart. Amount is the
// signed value shown as the label; Bar its magnitude. Half/OweX are SVG
// attribute strings for the diverging chart (a rect's width and, for a payable,
// its left edge), each as a percentage of the axis where the centre is 50%.
type ledgerBar struct {
	Name   string
	Amount decimal.Decimal
	Bar    decimal.Decimal
	Half   string // bar width = magnitude/max * 50%
	OweX   string // left edge for an "owe" bar = 50% − Half
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
	hasLedger := false // true if any single year has ledger activity
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
		if !led.IsZero() {
			hasLedger = true
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
	data["HasLedger"] = hasLedger
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
	// Per-neighbor Verrechnung: fetch unconditionally and derive the total, the
	// bar chart and HasLedger from it — so a year whose postings net to zero
	// across neighbors (e.g. +50/-50) still shows the chart instead of vanishing.
	results, err := s.store.YearNeighborResults(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	var ledgerSum, ledgerMax decimal.Decimal
	ledgerBars := make([]ledgerBar, 0, len(results))
	for _, res := range results {
		ledgerSum = ledgerSum.Add(res.Ledger)
		if res.Ledger.IsZero() {
			continue
		}
		abs := res.Ledger.Abs()
		ledgerBars = append(ledgerBars, ledgerBar{Name: res.Name, Amount: res.Ledger, Bar: abs})
		if abs.GreaterThan(ledgerMax) {
			ledgerMax = abs
		}
	}
	sort.Slice(ledgerBars, func(i, j int) bool { return ledgerBars[i].Bar.GreaterThan(ledgerBars[j].Bar) })
	// Precompute SVG geometry (half-axis width + owe left edge) as percentage
	// strings — set on the rect as attributes, so no CSP-blocked inline styles.
	fifty := decimal.NewFromInt(50)
	for i := range ledgerBars {
		half := decimal.Zero
		if ledgerMax.IsPositive() {
			half = ledgerBars[i].Bar.Div(ledgerMax).Mul(fifty)
		}
		ledgerBars[i].Half = half.StringFixed(2) + "%"
		ledgerBars[i].OweX = fifty.Sub(half).StringFixed(2) + "%"
	}

	// Mini trend sparklines from the per-year series (decorative — a failure
	// here simply omits them rather than failing the page).
	var revSpark, hoursSpark, netSpark *sparkView
	if totals, err := s.store.YearlyTotals(r.Context()); err == nil && len(totals) >= 2 {
		rev := make([]decimal.Decimal, len(totals))
		hrs := make([]decimal.Decimal, len(totals))
		net := make([]decimal.Decimal, len(totals))
		for i, t := range totals {
			rev[i], hrs[i], net[i] = t.Cost, t.Hours, t.Cost.Add(t.Ledger)
		}
		revSpark, hoursSpark, netSpark = makeSpark(rev), makeSpark(hrs), makeSpark(net)
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
	data["RevSpark"] = revSpark
	data["HoursSpark"] = hoursSpark
	data["NetSpark"] = netSpark
	data["HasLedger"] = len(ledgerBars) > 0
	data["LedgerBars"] = ledgerBars
	data["LedgerBarsMax"] = ledgerMax
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
