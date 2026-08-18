package server

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// sparkView holds the SVG geometry for a tile's trend sparkline (viewBox
// 0 0 100 32). Everything rides on SVG attributes — CSP-safe. End marks the
// current (last) year for the endpoint dot; Points carry per-year tooltip
// labels and a full-height hit column for hover/tap.
type sparkView struct {
	Line, Area string
	EndX, EndY string
	Points     []sparkPoint
}

type sparkPoint struct {
	X, Y, Tip  string
	HitX, HitW string // full-height hover/tap column around the point
}

// makeSpark normalises a value series into an SVG sparkline. Returns nil for
// fewer than three points — two years is a single straight segment that adds
// nothing the delta chip doesn't, so the KPI falls back to a prev-vs-current
// bar pair (makeBarPair). years/fmtVal build the per-point tooltip.
func makeSpark(vals []decimal.Decimal, years []int, fmtVal func(decimal.Decimal) string) *sparkView {
	n := len(vals)
	if n < 3 {
		return nil
	}
	fs := make([]float64, n)
	lo, hi := math.Inf(1), math.Inf(-1)
	for i, v := range vals {
		f, _ := v.Float64()
		fs[i] = f
		lo, hi = min(lo, f), max(hi, f)
	}
	rng := hi - lo
	half := 50.0 / float64(n-1)
	var b strings.Builder
	pts := make([]sparkPoint, n)
	var lastX, lastY float64
	for i, f := range fs {
		x := float64(i) / float64(n-1) * 100
		y := 15.5 // a flat (no-variance) series draws a centered line
		if rng > 0 {
			y = 29 - (f-lo)/rng*27 // y in ~2..29 (SVG y grows downward)
		}
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%.1f,%.1f", x, y)
		tip := ""
		if i < len(years) {
			tip = strconv.Itoa(years[i]) + " · " + fmtVal(vals[i])
		}
		hx := max(0, x-half)
		pts[i] = sparkPoint{
			X: fmt.Sprintf("%.1f", x), Y: fmt.Sprintf("%.1f", y), Tip: tip,
			HitX: fmt.Sprintf("%.1f", hx), HitW: fmt.Sprintf("%.1f", min(100, x+half)-hx),
		}
		lastX, lastY = x, y
	}
	line := b.String()
	return &sparkView{
		Line: line, Area: line + " 100.0,32 0.0,32",
		EndX: fmt.Sprintf("%.1f", lastX), EndY: fmt.Sprintf("%.1f", lastY),
		Points: pts,
	}
}

// barPair is a two-bar "previous vs current" mini chart for a KPI tile, shown
// when there aren't yet enough years for a trend line. Geometry is precomputed
// (SVG viewBox 0 0 40 34, bars grow up from y=32) so no CSP-blocked inline
// style is needed.
type barPair struct {
	PrevH, PrevY, CurH, CurY string
	PrevVal, CurVal          string // formatted labels (money / hours)
	PrevYear, CurYear        int
}

func makeBarPair(prev, cur decimal.Decimal, prevYear, curYear int, fmtVal func(decimal.Decimal) string) *barPair {
	pf, _ := prev.Float64()
	cf, _ := cur.Float64()
	peak := max(pf, cf)
	if peak <= 0 {
		peak = 1
	}
	const base, maxH = 32.0, 28.0
	ph := max(2, pf/peak*maxH)
	ch := max(2, cf/peak*maxH)
	f := func(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
	return &barPair{
		PrevH: f(ph), PrevY: f(base - ph),
		CurH: f(ch), CurY: f(base - ch),
		PrevVal: fmtVal(prev), CurVal: fmtVal(cur),
		PrevYear: prevYear, CurYear: curYear,
	}
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
// its left edge), each as a percentage of the axis where the center is 50%.
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

// maxHours returns the largest hours value in the rows (for scaling an
// hours-based bar chart).
func maxHours(rows []aggRow) decimal.Decimal {
	m := decimal.Zero
	for _, r := range rows {
		if r.Hours.GreaterThan(m) {
			m = r.Hours
		}
	}
	return m
}

// aggregateMachineHours sums each booking's hours onto every machine it ran (a
// booking's MachineLabels is the comma-joined set). Cost is intentionally left out:
// a rig's cost belongs to the whole combination, not one machine, so only the
// unambiguous hours-of-use is reported. Rows are sorted by hours descending.
func aggregateMachineHours(entries []models.Entry) []aggRow {
	order := []string{}
	byKey := map[string]*aggRow{}
	for _, e := range entries {
		if e.Voided || strings.TrimSpace(e.MachineLabels) == "" {
			continue
		}
		for _, name := range strings.Split(e.MachineLabels, ",") {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			row, ok := byKey[name]
			if !ok {
				row = &aggRow{Label: name}
				byKey[name] = row
				order = append(order, name)
			}
			row.Hours = row.Hours.Add(e.Hours)
		}
	}
	out := make([]aggRow, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hours.GreaterThan(out[j].Hours) })
	return out
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
	Credit    decimal.Decimal // per-neighbor overpayments/credits, netted out of OpenCost
	Completed bool
	PaidPct   string // paid share of (paid+open), e.g. "43.0%", for the pay bar
}

// payPct returns the paid share of (paid+open) as an SVG-width string clamped
// to 0–100%.
func payPct(paid, open decimal.Decimal) string {
	total := paid.Add(open)
	if total.IsZero() {
		return "0%"
	}
	p := paid.Div(total).Mul(decimal.NewFromInt(100))
	if p.GreaterThan(decimal.NewFromInt(100)) {
		p = decimal.NewFromInt(100)
	}
	if p.IsNegative() {
		p = decimal.Zero
	}
	return p.StringFixed(1) + "%"
}

// handleStatsAll renders a cross-year overview: per-year revenue, hours and
// paid/open split, a revenue-per-year bar chart, and grand totals.
func (s *Server) handleStatsAll(w http.ResponseWriter, r *http.Request) {
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	stats := make([]yearStat, 0, len(years))
	revenue := make([]aggRow, 0, len(years))
	var grandCost, grandHours, grandLedger, grandPaid, grandOpen, grandCredit decimal.Decimal
	hasLedger := false // true if any single year has ledger activity
	// One aggregate query for every year's bookings/hours/ledger, keyed by year
	// id, instead of loading all entry rows per year (N+1).
	totals, err := s.store.YearlyTotals(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	totalByID := make(map[int64]store.YearTotal, len(totals))
	for _, t := range totals {
		totalByID[t.YearID] = t
	}
	for _, y := range years {
		t := totalByID[y.ID]
		cost, hours, led := t.Cost, t.Hours, t.Ledger
		paid, open, credit, err := s.store.YearPaymentTotals(r.Context(), y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if !led.IsZero() {
			hasLedger = true
		}
		stats = append(stats, yearStat{
			Year: y.Year, YearID: y.ID, Cost: cost, Hours: hours,
			Ledger: led, Net: cost.Add(led),
			PaidCost: paid, OpenCost: open, Credit: credit, Completed: y.Completed(),
			PaidPct: payPct(paid, open),
		})
		revenue = append(revenue, aggRow{Label: strconv.Itoa(y.Year), Hours: hours, Cost: cost})
		grandCost = grandCost.Add(cost)
		grandHours = grandHours.Add(hours)
		grandLedger = grandLedger.Add(led)
		grandPaid = grandPaid.Add(paid)
		grandOpen = grandOpen.Add(open)
		grandCredit = grandCredit.Add(credit)
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
	data["GrandCredit"] = grandCredit
	data["GrandPaidPct"] = payPct(grandPaid, grandOpen)
	s.render(w, r, "stats_all", data)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	entries, err := s.store.ListEntriesByYear(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
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
	// Stunden je Maschine: a booking can run several machines (MachineLabels is the
	// comma-joined set), and its cost is for the whole rig — so cost can't be split
	// per machine cleanly. Hours-of-use CAN: attribute the booking's hours to each
	// machine it ran. Answers "which machine is used most this year".
	byMachine := aggregateMachineHours(entries)

	// Payment split (paid vs open), computed in a single query.
	paidCost, openCost, creditCost, err := s.store.YearPaymentTotals(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Per-neighbor Verrechnung: fetch unconditionally and derive the total, the
	// bar chart and HasLedger from it — so a year whose postings net to zero
	// across neighbors (e.g. +50/-50) still shows the chart instead of vanishing.
	results, err := s.store.YearNeighborResults(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
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
	if totals, err := s.store.YearlyTotals(r.Context()); err == nil && len(totals) >= 3 {
		rev := make([]decimal.Decimal, len(totals))
		hrs := make([]decimal.Decimal, len(totals))
		net := make([]decimal.Decimal, len(totals))
		years := make([]int, len(totals))
		for i, t := range totals {
			rev[i], hrs[i], net[i] = t.Cost, t.Hours, t.Cost.Add(t.Ledger)
			years[i] = t.Year
		}
		hoursFmt := func(v decimal.Decimal) string { return web.Num(v) + " h" }
		revSpark = makeSpark(rev, years, web.Money)
		hoursSpark = makeSpark(hrs, years, hoursFmt)
		netSpark = makeSpark(net, years, web.Money)
	}

	data := s.newPage(w, r, "Statistik", "stats")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["TotalCost"] = totalCost
	data["TotalHours"] = totalHours
	data["PaidCost"] = paidCost
	data["OpenCost"] = openCost
	data["CreditCost"] = creditCost
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
	data["ByMachine"] = byMachine
	data["ByMachineMax"] = maxHours(byMachine)

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
		data["RevPair"] = makeBarPair(pc, totalCost, prev.Year, year.Year, web.Money)
		data["HoursPair"] = makeBarPair(ph, totalHours, prev.Year, year.Year, func(v decimal.Decimal) string { return web.Num(v) + " h" })
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
