package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestStatsAllKeepsYearNavigationIntegration prevents the cross-year report
// from dropping the persistent year and reporting shortcuts.
func TestStatsAllKeepsYearNavigationIntegration(t *testing.T) {
	e := newItEnv(t)
	page := e.get(fmt.Sprintf("/stats/all?year=%d", e.yearID64))
	for _, want := range []string{
		`class="yearbar"`,
		`action="/stats"`,
		fmt.Sprintf(`href="/stats?year=%d"`, e.yearID64),
		fmt.Sprintf(`href="/stats/all?year=%d"`, e.yearID64),
		`class="yearquick is-active"`,
		`aria-current="page"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("cross-year statistics page missing %q", want)
		}
	}
}

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

	// Machine usage: the rig's machine ran 3 × 2 h = 6 h at the frozen
	// component rate 2 m × 5 €/m·h = 10 €/h → 60,00 revenue.
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
	if u.Estimated {
		t.Error("new bookings with component snapshots are marked as estimates")
	}

	// A later catalog change must not rewrite those three historical bookings.
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"7"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"4"},
	})
	usage, err = e.st.MachineUsageForYear(e.ctx, yid, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("machine usage after self cost: %v (n=%d)", err, len(usage))
	}
	u = usage[0]
	if u.Revenue.StringFixed(2) != "60.00" || u.HasMargin() {
		t.Errorf("catalog edit rewrote frozen usage: revenue=%s cost=%s", u.Revenue, u.Cost)
	}

	// A booking saved after the change freezes 14 €/h revenue and 4 €/h own
	// cost. The row has only partial own-cost history, so it must not claim a
	// complete contribution margin for the earlier zero-cost snapshots.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-08-10"},
		"hours": {"2"}, "unit": {"h"},
	})
	usage, err = e.st.MachineUsageForYear(e.ctx, yid, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("machine usage with new snapshot: %v (n=%d)", err, len(usage))
	}
	u = usage[0]
	if u.HasMargin() || u.Revenue.StringFixed(2) != "88.00" || u.Cost.StringFixed(2) != "8.00" {
		t.Errorf("partial snapshot coverage = revenue %s, cost %s, complete=%v", u.Revenue, u.Cost, u.HasMargin())
	}
	// Resetting the catalog still leaves every saved snapshot unchanged.
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"5"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"0"},
	})
	usage, err = e.st.MachineUsageForYear(e.ctx, yid, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("machine usage after second catalog edit: %v (n=%d)", err, len(usage))
	}
	if usage[0].Revenue.StringFixed(2) != "88.00" || usage[0].Cost.StringFixed(2) != "8.00" {
		t.Errorf("second catalog edit changed historical snapshots: usage=%+v", usage)
	}

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
	if !strings.Contains(csvBody, "historischer Snapshot") {
		t.Error("CSV does not identify frozen historical component values")
	}
	if strings.Contains(page, "Altbestand geschätzt") {
		t.Error("page marks new snapshotted bookings as legacy estimates")
	}
}

// A complete own-cost snapshot remains a valid contribution margin after the
// catalog value changes later.
func TestMachineUsageSnapshotMarginIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"5"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"4"},
	})
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-06-01"},
		"hours": {"2"}, "unit": {"h"},
	})
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"7"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"0"},
	})
	usage, err := e.st.MachineUsageForYear(e.ctx, e.yearID64, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("snapshot margin usage: %v (n=%d)", err, len(usage))
	}
	u := usage[0]
	if !u.HasMargin() || u.Revenue.StringFixed(2) != "20.00" || u.Cost.StringFixed(2) != "8.00" || u.Margin.StringFixed(2) != "12.00" {
		t.Errorf("frozen margin = %s − %s = %s complete=%v, want 20.00 − 8.00 = 12.00", u.Revenue, u.Cost, u.Margin, u.HasMargin())
	}
	page := e.get(fmt.Sprintf("/stats?year=%d", e.yearID64))
	if !strings.Contains(page, "Deckungsbeitrag") {
		t.Error("the statistics page does not show the complete frozen margin")
	}
}

// Bookings created before component snapshots remain usable, but their machine
// allocation is explicitly marked as an estimate at the current catalog rate.
func TestMachineUsageLegacyFallbackIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-05-01"},
		"hours": {"2"}, "unit": {"h"},
	})
	if _, err := e.pool.ExecContext(e.ctx, `
		DELETE FROM entry_machine_snapshots
		 WHERE entry_id IN (
		       SELECT id FROM entries WHERE billing_year_id=$1 AND neighbor_id=$2
		 )`, e.yearID64, e.neighborID); err != nil {
		t.Fatalf("simulate legacy booking: %v", err)
	}
	e.post("/prices/machines", url.Values{
		"base_id": {itoa64(e.baseID64)}, "id": {itoa64(e.machineID)},
		"name": {"IT-Maschine"}, "working_width": {"2"}, "cost_per_ab": {"7"},
		"category": {"IT"}, "sort_order": {"1"}, "self_cost_per_h": {"4"},
	})
	usage, err := e.st.MachineUsageForYear(e.ctx, e.yearID64, parseDay(""), parseDay(""))
	if err != nil || len(usage) != 1 {
		t.Fatalf("legacy machine usage: %v (n=%d)", err, len(usage))
	}
	if !usage[0].Estimated || usage[0].Revenue.StringFixed(2) != "28.00" || usage[0].Cost.StringFixed(2) != "8.00" {
		t.Errorf("legacy fallback = %+v, want current-rate estimate 28.00/8.00", usage[0])
	}
	page := e.get(fmt.Sprintf("/stats?year=%d", e.yearID64))
	if !strings.Contains(page, "Altbestand geschätzt") {
		t.Error("legacy fallback is not disclosed on the statistics page")
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
