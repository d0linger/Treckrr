//go:build integration

package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestBelegCalculationPathUsesSnapshots verifies the explanation is based on
// booking-time component values even after the live catalog changes.
func TestBelegCalculationPathUsesSnapshots(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {fmt.Sprintf("%d-06-01", e.year)},
		"hours": {"1.5"}, "unit": {"h"},
	})
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fixture entry: %v (%d)", err, len(entries))
	}
	snapshots, err := e.st.EntryMachineSnapshotsByNeighborYear(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(snapshots[entries[0].ID]) != 1 {
		t.Fatalf("component snapshots: %+v, %v", snapshots, err)
	}
	if snapshots[entries[0].ID][0].HourlyRate.StringFixed(2) != "10.00" {
		t.Fatalf("snapshot rate = %s, want 10.00", snapshots[entries[0].ID][0].HourlyRate)
	}
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE machines SET cost_per_ab=99 WHERE id=$1`, e.machineID); err != nil {
		t.Fatal(err)
	}
	page := e.get(fmt.Sprintf("/neighbors/%d/beleg?year=%d", e.neighborID, e.yearID64))
	wants := []string{
		"data-beleg-calculation",
		"Rechenweg",
		"Gebuchte Formel",
		"Rundung je Position",
		"Maschinen-Snapshots",
		"IT-Maschine",
	}
	for _, want := range wants {
		if !strings.Contains(page, want) {
			t.Errorf("calculation path missing %q", want)
		}
	}
}
