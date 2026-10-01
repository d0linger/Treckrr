//go:build integration

package server

import (
	"html"
	"net/url"
	"strings"
	"testing"
)

// TestEquipmentPricingConsistentAcrossEntryWorkflows is a characterization
// test for the two interactive paths that can book a fixed rig. Both paths must
// freeze the same catalog labels, hourly rate and final rounded cost before their
// shared resolution logic can be extracted safely.
func TestEquipmentPricingConsistentAcrossEntryWorkflows(t *testing.T) {
	e := newItEnv(t)

	e.post("/entries", url.Values{
		"booking_form_version": {"2"},
		"booking_kind":         {"equipment"},
		"booking_direction":    {"out"},
		"year_id":              {itoa64(e.yearID64)},
		"neighbor_id":          {itoa64(e.neighborID)},
		"entry_date":           {"2026-09-21"},
		"mode":                 {"gespann"},
		"gespann_id":           {itoa64(e.gespannID)},
		"hours":                {"2.345"},
	})
	e.post("/entries/quick", url.Values{
		"year_id":     {itoa64(e.yearID64)},
		"neighbor_id": {itoa64(e.neighborID)},
		"q_date":      {"2026-09-22"},
		"q_gespann":   {itoa64(e.gespannID)},
		"q_hours":     {"2.345"},
		"q_key":       {"pricing-consistency-" + e.uname},
	})

	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}

	byDate := make(map[string]int, len(entries))
	for i := range entries {
		byDate[entries[i].Date.Format("2006-01-02")] = i
	}
	standardIndex, standardOK := byDate["2026-09-21"]
	quickIndex, quickOK := byDate["2026-09-22"]
	if !standardOK || !quickOK {
		t.Fatalf("booked dates = %v, want standard and quick dates", byDate)
	}
	standard, quick := entries[standardIndex], entries[quickIndex]

	if !standard.HourlyRate.Equal(quick.HourlyRate) || !standard.Cost.Equal(quick.Cost) {
		t.Fatalf("pricing differs: standard %s h x %s = %s, quick %s h x %s = %s",
			standard.Hours, standard.HourlyRate, standard.Cost,
			quick.Hours, quick.HourlyRate, quick.Cost)
	}
	if standard.HourlyRate.StringFixed(2) != "46.00" || standard.Cost.StringFixed(2) != "107.87" {
		t.Fatalf("frozen price = %s x %s, want 46.00 x 2.345 = 107.87",
			standard.HourlyRate.StringFixed(2), standard.Cost.StringFixed(2))
	}
	if standard.TaskLabel != quick.TaskLabel || standard.TractorLabel != quick.TractorLabel ||
		standard.LoadLabel != quick.LoadLabel || standard.MachineLabels != quick.MachineLabels {
		t.Fatalf("snapshots differ: standard=%+v quick=%+v", standard, quick)
	}
	if standard.GespannID == nil || quick.GespannID == nil ||
		*standard.GespannID != e.gespannID || *quick.GespannID != e.gespannID {
		t.Fatalf("fixed rig identity not preserved: standard=%v quick=%v", standard.GespannID, quick.GespannID)
	}
}

// TestCatalogHoursPrecisionConsistentAcrossEntryWorkflows ensures all catalog-
// backed form adapters reject hours PostgreSQL cannot store exactly. Otherwise
// the database could round hours independently from the already-calculated cost.
func TestCatalogHoursPrecisionConsistentAcrossEntryWorkflows(t *testing.T) {
	e := newItEnv(t)
	base := url.Values{
		"year_id":     {itoa64(e.yearID64)},
		"neighbor_id": {itoa64(e.neighborID)},
		"entry_date":  {"2026-09-23"},
		"gespann_id":  {itoa64(e.gespannID)},
		"hours":       {"1.2345"},
	}

	legacyBody := html.UnescapeString(e.post("/entries", base))
	if !strings.Contains(legacyBody, "höchstens drei Nachkommastellen") {
		t.Fatalf("legacy form did not explain the precision rejection")
	}

	v2 := url.Values{}
	for key, values := range base {
		v2[key] = append([]string(nil), values...)
	}
	v2.Set("booking_form_version", "2")
	v2.Set("booking_kind", "equipment")
	v2.Set("booking_direction", "out")
	v2.Set("mode", "gespann")
	v2Body := html.UnescapeString(e.post("/entries", v2))
	if !strings.Contains(v2Body, "höchstens drei Nachkommastellen") {
		t.Fatalf("unified form did not explain the precision rejection")
	}

	quickBody := html.UnescapeString(e.post("/entries/quick", url.Values{
		"year_id":     {itoa64(e.yearID64)},
		"neighbor_id": {itoa64(e.neighborID)},
		"q_date":      {"2026-09-23"},
		"q_gespann":   {itoa64(e.gespannID)},
		"q_hours":     {"1.2345"},
	}))
	if !strings.Contains(quickBody, "höchstens drei Nachkommastellen") {
		t.Fatalf("quick entry did not explain the precision rejection")
	}

	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("unrepresentable hours created %d entries", len(entries))
	}
}
