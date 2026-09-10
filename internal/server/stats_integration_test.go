package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Statistics gained a period filter, drilldown links, a CSV export, the
// machine contribution margin and per-unit metrics (Ausbaukarte 82-84).
func TestStatsPeriodDrilldownExportIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// Three rig bookings in two months plus one unit booking.
	for _, d := range []string{"2026-03-05", "2026-03-06", "2026-07-10"} {
		e.post("/entries", url.Values{
			"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
			"gespann_id": {itoa64(e.gespannID)}, "entry_date": {d},
			"hours": {"2"}, "unit": {"h"},
		})
	}
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"entry_date": {"2026-03-07"}, "unit": {"ha"},
		"task_label": {"Grubbern"}, "quantity": {"4"}, "unit_price": {"25"},
	})

	// Whole year: 3 × 92,00 + 100,00 = 376,00.
	page := e.get(fmt.Sprintf("/stats?year=%d", yid))
	if !strings.Contains(page, "376,00") {
		t.Fatalf("year total 376,00 not on the statistics page")
	}
	// March only: 2 × 92,00 + 100,00 = 284,00.
	page = e.get(fmt.Sprintf("/stats?year=%d&from=2026-03-01&to=2026-03-31", yid))
	if !strings.Contains(page, "284,00") {
		t.Errorf("March window does not total 284,00")
	}
	if strings.Contains(page, "376,00") {
		t.Errorf("the filtered page still shows the whole-year total")
	}
	// Drilldown links carry the period through to the bookings list.
	if !strings.Contains(page, fmt.Sprintf("/buchungen?year=%d&amp;neighbor_id=%d&amp;from=2026-03-01", yid, nid)) {
		t.Errorf("neighbor bar has no drilldown link carrying the period")
	}

	// Per-unit metric: 4 ha for 100,00 = 25,00 je ha.
	if !strings.Contains(page, "Kennzahlen je Einheit") || !strings.Contains(page, "25,00 € / ha") {
		t.Errorf("per-unit metric missing (want 25,00 / ha)")
	}

	// Machine usage: the rig's machine ran 3 × 2 h = 6 h at 2 m × 5 €/m·h = 10 €/h
	// → 60,00 estimated revenue at current rates, not frozen booked revenue.
	usage, err := e.st.MachineUsageForYear(e.ctx, yid, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("machine usage: %v (n=%d)", err, len(usage))
	}
	u := usage[0]
	if u.Hours.StringFixed(2) != "6.00" || u.Rate.StringFixed(2) != "10.00" || u.Revenue.StringFixed(2) != "60.00" {
		t.Errorf("machine usage = %s h × %s = %s, want 6.00 × 10.00 = 60.00", u.Hours, u.Rate, u.Revenue)
	}
	if u.HasMargin() {
		t.Errorf("a contribution margin is claimed although no self cost is configured")
	}

	// Configure self costs: 4 €/h → 6 h × 4 = 24,00 cost, 36,00 margin.
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"5"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"4"},
	})
	usage, err = e.st.MachineUsageForYear(e.ctx, yid, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("machine usage after self cost: %v (n=%d)", err, len(usage))
	}
	u = usage[0]
	if !u.HasMargin() || u.Cost.StringFixed(2) != "24.00" || u.Margin.StringFixed(2) != "36.00" {
		t.Errorf("margin = %s − %s = %s, want 60.00 − 24.00 = 36.00", u.Revenue, u.Cost, u.Margin)
	}
	if page := e.get(fmt.Sprintf("/stats?year=%d", yid)); !strings.Contains(page, "Deckungsbeitrag") {
		t.Errorf("the statistics page does not show the contribution margin")
	}
	// Reset for the export below. newItEnv's cleanup removes this test's entire
	// price base even after Fatal; it is never shared with another test.
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"5"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"0"},
	})

	// CSV export carries the sections and honors the period.
	csvBody := e.get(fmt.Sprintf("/stats/export.csv?year=%d&from=2026-03-01&to=2026-03-31", yid))
	if !strings.Contains(csvBody, "Auswertung;Bezeichnung") {
		t.Fatalf("stats CSV has no header")
	}
	for _, want := range []string{"Nachbar", "Tätigkeit", "Maschine", "Kennzahl", "Grubbern"} {
		if !strings.Contains(csvBody, want) {
			t.Errorf("stats CSV is missing the %q section", want)
		}
	}
	if strings.Contains(csvBody, "376,00") {
		t.Errorf("the filtered CSV contains the whole-year total")
	}
	if !strings.Contains(csvBody, "Schätzung zu aktuellen Sätzen") {
		t.Error("CSV presents current-price machine allocations as historical revenue")
	}
	if !strings.Contains(page, "Schätzungen zu aktuellen Sätzen") {
		t.Error("page does not disclose mutable-rate estimates")
	}
}

// A neighbor's multi-year trend (Ausbaukarte 85).
func TestNeighborTrendIntegration(t *testing.T) {
	e := newItEnv(t)
	nid := e.neighborID

	// The itEnv year plus a second one the same neighbor takes part in.
	base2, err := e.st.CreateEmptyBase(e.ctx, e.year+1, "Trend-Basis "+e.uname)
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	year2, err := e.st.CreateBillingYear(e.ctx, e.year+1, base2, "Trend-Jahr "+e.uname)
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM entries WHERE billing_year_id=$1`, year2)
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM billing_year_neighbors WHERE billing_year_id=$1`, year2)
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM billing_years WHERE id=$1`, year2)
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM price_bases WHERE id=$1`, base2)
	})
	if err := e.st.AddNeighborToYear(e.ctx, year2, nid); err != nil {
		t.Fatalf("add to year: %v", err)
	}
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-04-01"},
		"hours": {"1"}, "unit": {"h"},
	})

	series, err := e.st.NeighborYearSeries(e.ctx, nid)
	if err != nil || len(series) != 2 {
		t.Fatalf("series: %v (n=%d, want both years)", err, len(series))
	}
	if series[0].Cost.StringFixed(2) != "46.00" || !series[1].Cost.IsZero() {
		t.Errorf("series costs = %s / %s, want 46.00 / 0", series[0].Cost, series[1].Cost)
	}
	page := e.get(fmt.Sprintf("/neighbors/%d/overview", nid))
	if !strings.Contains(page, "Verlauf über die Jahre") {
		t.Errorf("the overview shows no multi-year trend although the neighbor is in two years")
	}
}
