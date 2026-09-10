package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// Personenstamm, Mannstunden and the Anfahrt surcharge through the real
// handlers (Ausbaukarte 56/57/58). The point of these bookings is that they are
// ORDINARY entries — so the check is that the money lands in the same totals
// every other booking does, priced from the master data.
func TestPersonsAndSurchargesIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	pname := "Helfer " + e.uname
	t.Cleanup(func() {
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM persons WHERE name LIKE '%'||$1`, e.uname)
	})

	// Master data: one helper at 18,00 €/h, through the page.
	e.post("/personen", url.Values{"name": {pname}, "hourly_rate": {"18"}, "note": {"Aushilfe"}})
	persons, err := e.st.ActivePersons(e.ctx)
	if err != nil {
		t.Fatalf("persons: %v", err)
	}
	var pid int64
	for _, p := range persons {
		if p.Name == pname {
			pid = p.ID
			if p.HourlyRate.StringFixed(2) != "18.00" {
				t.Errorf("rate = %s, want 18.00", p.HourlyRate.StringFixed(2))
			}
		}
	}
	if pid == 0 {
		t.Fatalf("person %q was not created", pname)
	}
	if page := e.get("/personen"); !strings.Contains(page, pname) {
		t.Errorf("the Personen page does not list the new helper")
	}

	// 2,5 Mannstunden priced from the master data: 2,5 × 18 = 45,00.
	e.post(fmt.Sprintf("/neighbors/%d/mannstunden", nid), url.Values{
		"year_id": {itoa64(yid)}, "person_id": {itoa64(pid)},
		"hours": {"2.5"}, "entry_date": {"2026-08-01"},
	})
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	me := entries[0]
	if me.Unit != "Mannstunde" || me.Cost.StringFixed(2) != "45.00" || me.UnitPrice.StringFixed(2) != "18.00" {
		t.Errorf("Mannstunden booking = %s %s × %s = %s", me.Unit, me.Quantity, me.UnitPrice, me.Cost)
	}
	if me.PersonID == nil || *me.PersonID != pid {
		t.Errorf("booking is not attributed to the person: %v", me.PersonID)
	}
	// A one-off different rate must win over the master data.
	e.post(fmt.Sprintf("/neighbors/%d/mannstunden", nid), url.Values{
		"year_id": {itoa64(yid)}, "person_id": {itoa64(pid)},
		"hours": {"1"}, "hourly_rate": {"25"}, "entry_date": {"2026-08-02"},
	})
	entries, _ = e.st.ListEntries(e.ctx, nid, yid)
	if len(entries) != 2 {
		t.Fatalf("second booking missing (n=%d)", len(entries))
	}
	var override bool
	for _, en := range entries {
		if en.UnitPrice.StringFixed(2) == "25.00" && en.Cost.StringFixed(2) == "25.00" {
			override = true
		}
	}
	if !override {
		t.Errorf("the one-off rate did not win over the master data")
	}

	// Per-person aggregation for the year: 3,5 h / 70,00 €.
	hours, err := e.st.PersonHoursForYear(e.ctx, yid)
	if err != nil || len(hours) != 1 {
		t.Fatalf("person hours: %v (n=%d)", err, len(hours))
	}
	if hours[0].Hours.StringFixed(2) != "3.50" || hours[0].Cost.StringFixed(2) != "70.00" {
		t.Errorf("aggregation = %s h / %s €, want 3.50 / 70.00", hours[0].Hours, hours[0].Cost)
	}

	// Anfahrt is hidden until rates exist, then books flat and per-km.
	pageURL := fmt.Sprintf("/neighbors/%d?year=%d", nid, yid)
	if page := e.get(pageURL); strings.Contains(page, "Anfahrt verrechnen") {
		t.Errorf("the Anfahrt form is offered although no rates are configured")
	}
	e.post(fmt.Sprintf("/neighbors/%d/anfahrt", nid), url.Values{"year_id": {itoa64(yid)}})
	if again, _ := e.st.ListEntries(e.ctx, nid, yid); len(again) != 2 {
		t.Errorf("an Anfahrt was booked without a configured rate (n=%d)", len(again))
	}
	e.post("/admin/company", url.Values{
		"name": {"IT-Betrieb"}, "address": {"Hofstraße 2, 4710 Testdorf"},
		"tax_id": {"ATU00000000"}, "tax_mode": {"pauschal"}, "vat_rate": {"13"},
		"payment_term_days": {"14"}, "travel_flat": {"15"}, "travel_per_km": {"0.5"},
	})
	if page := e.get(pageURL); !strings.Contains(page, "Anfahrt verrechnen") {
		t.Errorf("the Anfahrt form stays hidden although rates are configured")
	}
	e.post(fmt.Sprintf("/neighbors/%d/anfahrt", nid), url.Values{
		"year_id": {itoa64(yid)}, "entry_date": {"2026-08-03"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/anfahrt", nid), url.Values{
		"year_id": {itoa64(yid)}, "km": {"12"}, "entry_date": {"2026-08-04"},
	})
	entries, _ = e.st.ListEntries(e.ctx, nid, yid)
	if len(entries) != 4 {
		t.Fatalf("expected 4 bookings, got %d", len(entries))
	}
	var flat, perKm bool
	for _, en := range entries {
		if en.Unit == "Anfahrt" && en.Cost.StringFixed(2) == "15.00" {
			flat = true
		}
		if en.Unit == "km" && en.Cost.StringFixed(2) == "6.00" { // 12 × 0,50
			perKm = true
		}
	}
	if !flat || !perKm {
		t.Errorf("Anfahrt bookings wrong: flat=%v perKm=%v", flat, perKm)
	}

	// They are ordinary bookings, so they are in the neighbor's total:
	// 45 + 25 + 15 + 6 = 91,00.
	cost, _, err := e.st.NeighborTotal(e.ctx, nid, yid)
	if err != nil {
		t.Fatalf("total: %v", err)
	}
	if cost.StringFixed(2) != "91.00" {
		t.Errorf("neighbor total = %s, want 91.00", cost.StringFixed(2))
	}

	// A booked person may not be deleted — archived instead, which also takes
	// them out of the booking form.
	e.post(fmt.Sprintf("/personen/%d/delete", pid), url.Values{})
	if p, err := e.st.GetPerson(e.ctx, pid); err != nil || p == nil {
		t.Errorf("a booked person was deleted: %v", err)
	}
	e.post(fmt.Sprintf("/personen/%d/archive", pid), url.Values{"archived": {"true"}})
	active, _ := e.st.ActivePersons(e.ctx)
	for _, p := range active {
		if p.ID == pid {
			t.Errorf("the archived person is still offered for booking")
		}
	}
	// Restore the company defaults so parallel expectations stay untouched.
	e.post("/admin/company", url.Values{
		"name": {"IT-Betrieb"}, "address": {"Hofstraße 2, 4710 Testdorf"},
		"tax_id": {"ATU00000000"}, "tax_mode": {"pauschal"}, "vat_rate": {"13"},
		"payment_term_days": {"14"},
	})
}
