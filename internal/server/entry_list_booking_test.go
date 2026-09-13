package server

import (
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// TestBookingFilterFromQuery keeps new choices allowlisted and pagination safe.
func TestBookingFilterFromQuery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, query, direction, kind string
		offset                       int
	}{
		{name: "incoming_equipment", query: "direction=in&kind=equipment&page=3", direction: "in", kind: "equipment", offset: 100},
		{name: "outgoing_cost", query: "direction=out&kind=fixed", direction: "out", kind: "fixed"},
		{name: "unknown_choices", query: "direction=sideways&kind=DROP"},
		{name: "negative_page", query: "page=-7"},
		{name: "overflow_page", query: "page=" + strconv.Itoa(int(^uint(0)>>1))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest("GET", "/buchungen?"+tc.query, nil)
			f := bookingFilterFromQuery(r, 42)
			if f.YearID != 42 || f.Direction != tc.direction || f.Kind != tc.kind || f.Offset != tc.offset {
				t.Errorf("unexpected parsed filter: %#v", f)
			}
		})
	}
}

// TestBookingListURLPreservesFilters guards sort/pager context in both directions.
func TestBookingListURLPreservesFilters(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "/buchungen?direction=in&kind=labor&unit=h&task=50%25&sort=cost&dir=desc&export=csv", nil)
	u, err := url.Parse(entryListURL(r, 42, 3, "cost"))
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for key, want := range map[string]string{"year": "42", "direction": "in", "kind": "labor", "unit": "h", "task": "50%", "sort": "cost", "dir": "", "page": "3", "export": ""} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}
