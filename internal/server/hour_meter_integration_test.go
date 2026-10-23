//go:build integration

package server

import (
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

// TestHourMeterIntegration pins the hour-meter readings: an own tractor
// booking stores both readings or none, the form receives the tractor's last
// end reading for the next start, an edit keeps them and a copy starts fresh,
// the older form keeps them, and the data-quality page names an overlap, an
// unbooked gap and a reading that disagrees with the booked hours.
func TestHourMeterIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	byDate := func() map[string]models.Entry {
		t.Helper()
		entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]models.Entry{}
		for _, en := range entries {
			out[en.Date.Format("2006-01-02")] = en
		}
		return out
	}
	book := func(date, hours, start, end string) {
		e.post("/entries", url.Values{"booking_form_version": {"3"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
			"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {date}, "mode": {"gespann"},
			"gespann_id": {itoa64(e.gespannID)}, "hours": {hours}, "meter_start": {start}, "meter_end": {end}})
	}

	book("2026-07-01", "2", "100", "")      // one-sided: refused
	book("2026-07-02", "2", "100", "99,5")  // end below start: refused
	book("2026-07-03", "2", "100", "102")   // fine
	book("2026-07-04", "2", "101,5", "103") // overlaps the 3rd (101,5 < 102)
	book("2026-07-05", "1", "110", "111")   // 7 h gap after 103
	book("2026-07-06", "1", "111", "114")   // 3 h on the meter, 1 h booked
	book("2026-07-07", "2", "", "")         // no readings at all
	got := byDate()
	for _, refused := range []string{"2026-07-01", "2026-07-02"} {
		if _, ok := got[refused]; ok {
			t.Errorf("%s: invalid readings were booked", refused)
		}
	}
	first := got["2026-07-03"]
	if !first.HasMeter() || first.MeterStart.Decimal.String() != "100" || first.MeterEnd.Decimal.String() != "102" || first.MeterHours().String() != "2" {
		t.Fatalf("readings not stored: %+v %+v", first.MeterStart, first.MeterEnd)
	}
	if plain := got["2026-07-07"]; plain.HasMeter() {
		t.Error("a booking without readings got some")
	}

	form := html.UnescapeString(e.get(neighborBookingURL(e.neighborID, e.yearID64)))
	if !strings.Contains(form, `data-meter-last="{"`+itoa64(e.tractorID)+`":"114"}"`) {
		t.Error("booking form does not carry the tractor's last end reading")
	}
	edit := e.get("/entries/" + itoa64(first.ID) + "/edit")
	if !strings.Contains(edit, `name="meter_start" min="0" step="0.01" inputmode="decimal" value="100"`) {
		t.Error("edit form does not show the stored readings")
	}
	if cp := e.get("/entries/" + itoa64(first.ID) + "/copy"); strings.Contains(cp, `inputmode="decimal" value="100"`) {
		t.Error("a copy repeats the source's readings")
	}

	// The older form has no meter fields; an edit through it keeps the readings.
	e.post("/entries/"+itoa64(first.ID)+"/update", url.Values{"year_id": {yid}, "neighbor_id": {nid},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-07-03"}, "hours": {"2"}, "unit": {"h"}})
	if kept := byDate()["2026-07-03"]; !kept.HasMeter() {
		t.Error("an older-form edit dropped the readings")
	}

	quality := html.UnescapeString(e.get("/data-quality?year=" + yid))
	for _, want := range []string{
		"Zählerstände überschneiden sich", "04.07.2026 · Anfang 101,5 h vor Ende 102 h",
		"Zählerstunden ohne Buchung", "1 Lücke(n), 7 h",
		"Stunden weichen vom Zähler ab", "06.07.2026 · Zähler 3 h, gebucht 1 h",
	} {
		if !strings.Contains(quality, want) {
			t.Errorf("data-quality page lacks %q", want)
		}
	}
	if strings.Contains(quality, "03.07.2026 · Zähler") {
		t.Error("a matching reading was flagged")
	}
	if beleg := e.get("/neighbors/" + nid + "/beleg?year=" + yid); !strings.Contains(beleg, "Betriebsstundenzähler") {
		t.Error("Beleg calculation path does not show the readings")
	}
}
