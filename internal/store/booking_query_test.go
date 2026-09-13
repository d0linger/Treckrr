package store

import (
	"slices"
	"strings"
	"testing"
)

// TestBookingFilterWhere verifies literal shared search and rejects untrusted
// direction/type fragments before they can become SQL predicates.
func TestBookingFilterWhere(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		f    BookingFilter
		args []any
		want string
	}{
		{name: "all_sources", f: BookingFilter{EntryFilter: EntryFilter{YearID: 7}}, args: []any{int64(7)}, want: " WHERE e.billing_year_id = $1"},
		{name: "incoming_labor", f: BookingFilter{EntryFilter: EntryFilter{YearID: 7}, Direction: "in", Kind: "labor"}, args: []any{int64(7), "in", "labor"}, want: " WHERE e.billing_year_id = $1 AND e.direction = $2 AND e.kind = $3"},
		{name: "malicious_choices", f: BookingFilter{EntryFilter: EntryFilter{YearID: 7}, Direction: "in OR true", Kind: "cost; DELETE FROM entries"}, args: []any{int64(7)}, want: " WHERE e.billing_year_id = $1"},
		{name: "literal_shared_search", f: BookingFilter{EntryFilter: EntryFilter{YearID: 7, Unit: "ha", Task: ` 50%_A\B `, Voided: "hide"}}, args: []any{int64(7), "ha", `%50\%\_a\\b%`},
			want: ` WHERE e.billing_year_id = $1 AND e.unit = $2 AND NOT e.voided AND (lower(e.task_label) LIKE $3 ESCAPE '\' OR lower(e.note) LIKE $3 ESCAPE '\' OR lower(e.detail) LIKE $3 ESCAPE '\')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			where, args := bookingFilterWhere(tc.f)
			if where != tc.want || !slices.Equal(args, tc.args) {
				t.Errorf("where/args = %q, %#v; want %q, %#v", where, args, tc.want, tc.args)
			}
		})
	}
}

// TestBookingFilterOrder keeps pages deterministic when source IDs overlap.
func TestBookingFilterOrder(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"date", "cost", "neighbor", "cost; DROP TABLE entries"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			order := bookingFilterOrder(BookingFilter{EntryFilter: EntryFilter{Sort: key, Desc: true}})
			if !strings.HasSuffix(order, ", e.source") || strings.Contains(order, ";") {
				t.Errorf("unsafe or unstable ordering %q", order)
			}
		})
	}
}

// TestBookingRowLabels preserves distinct financial meaning for all sources.
func TestBookingRowLabels(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, kind, label string
	}{
		{name: "equipment", kind: "equipment", label: "Traktor / Geräte"},
		{name: "labor", kind: "labor", label: "Mannstunden"},
		{name: "quantity", kind: "quantity", label: "Mengenleistung"},
		{name: "fixed", kind: "fixed", label: "Freie Kosten"},
		{name: "transfer", kind: "transfer", label: "Jahresübertrag"},
		{name: "legacy", kind: "manual", label: "Manuelle Position"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := BookingRow{Kind: tc.kind, Source: "ledger", Direction: "in"}
			if r.KindLabel() != tc.label || !r.IsLedger() || r.DirectionLabel() != "Ich schulde" {
				t.Errorf("incorrect incoming labels: %#v", r)
			}
			r.Source, r.Direction = "entry", "out"
			if r.IsLedger() || r.DirectionLabel() != "Nachbar schuldet" {
				t.Errorf("incorrect outgoing labels: %#v", r)
			}
		})
	}
}
