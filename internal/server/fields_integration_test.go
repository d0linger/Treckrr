//go:build integration

package server

import (
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

// TestFieldsIntegration pins the field reference end to end: fields are kept
// per neighbor, an own booking names a listed field or a free-text one and
// keeps the name, an archived field is refused for new bookings but kept on
// edit, and the list, the Beleg and the statistics show the work per field.
func TestFieldsIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	fieldsURL := "/neighbors/" + nid + "/fields?year=" + yid
	byDate := func() map[string]models.Entry {
		t.Helper()
		entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]models.Entry{}
		for _, en := range entries {
			if en.PersonID == nil {
				out[en.Date.Format("2006-01-02")] = en
			}
		}
		return out
	}
	book := func(date, fieldID, label string) {
		e.post("/entries", url.Values{"booking_form_version": {"3"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
			"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {date}, "mode": {"gespann"},
			"gespann_id": {itoa64(e.gespannID)}, "hours": {"2"}, "field_id": {fieldID}, "field_label": {label}})
	}

	e.post(fieldsURL, url.Values{"name": {"Hinterfeld"}, "area_ha": {"2,5"}, "plot_number": {"12/3"}})
	e.post(fieldsURL, url.Values{"name": {"hinterfeld"}})
	e.post(fieldsURL, url.Values{"name": {"Ohne Fläche"}, "area_ha": {"-1"}})
	fields, err := e.st.ListFields(e.ctx, e.neighborID)
	if err != nil || len(fields) != 1 || fields[0].Name != "Hinterfeld" || fields[0].AreaHa.String() != "2.5" || fields[0].PlotNumber != "12/3" {
		t.Fatalf("fields = %+v, %v; want only Hinterfeld 2,5 ha", fields, err)
	}
	hinter := itoa64(fields[0].ID)
	if page := e.get("/neighbors/" + nid + "?year=" + yid); !strings.Contains(page, "Hinterfeld · 2,5 ha · Schlag 12/3") {
		t.Error("neighbor page does not list the field")
	}
	if form := e.get(neighborBookingURL(e.neighborID, e.yearID64)); !strings.Contains(form, `<option value="`+hinter+`" >Hinterfeld · 2,5 ha · Schlag 12/3</option>`) &&
		!strings.Contains(form, `<option value="`+hinter+`">Hinterfeld`) {
		t.Error("booking form does not offer the field")
	}

	book("2026-06-01", hinter, "")
	book("2026-06-02", "other", "Wiese am Bach")
	got := byDate()
	if en := got["2026-06-01"]; en.FieldID == nil || *en.FieldID != fields[0].ID || en.FieldLabel != "Hinterfeld" {
		t.Errorf("listed field not stored: %+v %q", en.FieldID, en.FieldLabel)
	}
	if en := got["2026-06-02"]; en.FieldID != nil || en.FieldLabel != "Wiese am Bach" {
		t.Errorf("free-text field not stored: %+v %q", en.FieldID, en.FieldLabel)
	}

	// Archived: no new booking may pick it, an existing one keeps it on edit.
	e.post("/neighbors/"+nid+"/fields/archive?year="+yid, url.Values{"field_id": {hinter}, "archived": {"1"}})
	book("2026-06-03", hinter, "")
	if _, ok := byDate()["2026-06-03"]; ok {
		t.Error("a new booking picked an archived field")
	}
	first := byDate()["2026-06-01"]
	edit := e.get("/entries/" + itoa64(first.ID) + "/edit")
	if !strings.Contains(edit, `value="`+hinter+`" selected`) {
		t.Error("edit form does not keep the archived field selected")
	}
	e.post("/entries/"+itoa64(first.ID)+"/update", url.Values{"booking_form_version": {"3"}, "booking_kind": {"equipment"},
		"booking_direction": {"out"}, "year_id": {yid}, "neighbor_id": {nid}, "entry_date": {"2026-06-01"}, "mode": {"gespann"},
		"gespann_id": {itoa64(e.gespannID)}, "hours": {"3"}, "field_id": {hinter}})
	if en := byDate()["2026-06-01"]; en.Hours.String() != "3" || en.FieldLabel != "Hinterfeld" {
		t.Errorf("edit with the archived field: %s h, field %q", en.Hours, en.FieldLabel)
	}

	// The older form has no field picker; editing through it keeps the field.
	free := byDate()["2026-06-02"]
	e.post("/entries/"+itoa64(free.ID)+"/update", url.Values{"year_id": {yid}, "neighbor_id": {nid},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-06-02"}, "hours": {"1"}, "unit": {"h"}})
	if en := byDate()["2026-06-02"]; en.Hours.String() != "1" || en.FieldLabel != "Wiese am Bach" {
		t.Errorf("older-form edit: %s h, field %q; want 1 h and the field kept", en.Hours, en.FieldLabel)
	}

	list := html.UnescapeString(e.get("/buchungen?year=" + yid + "&task=hinterfeld"))
	if !strings.Contains(list, "Feld: Hinterfeld") || strings.Contains(list, "Wiese am Bach") {
		t.Error("the booking list does not find bookings by field")
	}
	if beleg := e.get("/neighbors/" + nid + "/beleg?year=" + yid); !strings.Contains(beleg, "Feld: Wiese am Bach") {
		t.Error("Beleg does not show the field")
	}
	// 3 h × 46 €/h = 138 € on 2,5 ha = 55,20 € je ha.
	stats := html.UnescapeString(e.get("/stats?year=" + yid))
	for _, want := range []string{"Felder", "Hinterfeld", "2,5 ha", "138,00 €", "55,20 €", "Wiese am Bach"} {
		if !strings.Contains(stats, want) {
			t.Errorf("statistics lack %q", want)
		}
	}

	e.post("/neighbors/"+nid+"/fields/delete?year="+yid, url.Values{"field_id": {hinter}})
	if left, _ := e.st.ListFields(e.ctx, e.neighborID); len(left) != 1 {
		t.Error("a booked field was deleted")
	}
	e.post(fieldsURL, url.Values{"name": {"Neues Feld"}})
	left, _ := e.st.ListFields(e.ctx, e.neighborID)
	for _, f := range left {
		if f.Name == "Neues Feld" {
			e.post("/neighbors/"+nid+"/fields/delete?year="+yid, url.Values{"field_id": {itoa64(f.ID)}})
		}
	}
	if left, _ := e.st.ListFields(e.ctx, e.neighborID); len(left) != 1 {
		t.Errorf("an unused field was not deleted: %+v", left)
	}
}
