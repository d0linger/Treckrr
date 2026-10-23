//go:build integration

package server

import (
	"html"
	"net/url"
	"strings"
	"testing"
)

// TestBookingCalendarIntegration renders the month grid of the booking list:
// each day carries its count and sum under the list's filters, empty days are
// labeled as such, a day links to the list of that day with the same filters,
// the months page through, and a saved view remembers the calendar.
func TestBookingCalendarIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	book := func(date, hours string) {
		e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(e.gespannID)},
			"entry_date": {date}, "hours": {hours}, "unit": {"h"}})
	}
	book("2026-05-04", "1")
	book("2026-05-04", "2")
	book("2026-05-20", "1")

	page := html.UnescapeString(e.get("/buchungen?year=" + yid + "&view=kalender&month=2026-05"))
	for _, want := range []string{
		"Mai 2026", "Montag, 04.05.2026: 2 Buchung(en), 138,00 €", "Mittwoch, 20.05.2026: 1 Buchung(en), 46,00 €",
		"Dienstag, 05.05.2026: keine Buchung", "3 Buchung(en) an 2 Tag(en)",
		"from=2026-05-04", "month=2026-04", "month=2026-06", `name="view" value="kalender"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("calendar lacks %q", want)
		}
	}
	if !strings.Contains(page, `value="year=`+yid+`&view=kalender"`) && !strings.Contains(page, "view=kalender&year="+yid) {
		t.Error("a saved view would not remember the calendar")
	}

	// Filters narrow the grid exactly like the list: incoming work is none here.
	filtered := html.UnescapeString(e.get("/buchungen?year=" + yid + "&view=kalender&month=2026-05&direction=in"))
	if !strings.Contains(filtered, "Montag, 04.05.2026: keine Buchung") || !strings.Contains(filtered, "0 Buchung(en) an 0 Tag(en)") {
		t.Error("calendar ignores the direction filter")
	}
	// A period filter narrows the month; it never widens it.
	narrowed := html.UnescapeString(e.get("/buchungen?year=" + yid + "&view=kalender&month=2026-05&from=2026-05-10"))
	if !strings.Contains(narrowed, "Montag, 04.05.2026: keine Buchung") || !strings.Contains(narrowed, "Mittwoch, 20.05.2026: 1 Buchung(en)") {
		t.Error("calendar ignores the period filter")
	}

	day := html.UnescapeString(e.get("/buchungen?year=" + yid + "&from=2026-05-04&to=2026-05-04"))
	if !strings.Contains(day, "2 Buchung(en)") {
		t.Error("the day link does not list that day's bookings")
	}
}
