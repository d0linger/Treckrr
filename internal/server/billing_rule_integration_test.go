//go:build integration

package server

import (
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

// TestBillingRuleIntegration pins the basis' billing increment and minimum end
// to end: a booking stored before the rule keeps its hours until the reviewed
// recalc bills it, and every catalog entry point (older form, unified form,
// quick entry) bills new machine hours rounded up to the increment and at least
// the minimum. Agreed free-text rates are billed exactly as typed. A locked
// basis refuses the change.
func TestBillingRuleIntegration(t *testing.T) {
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
	legacy := func(date, hours string) {
		e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid},
			"gespann_id": {itoa64(e.gespannID)}, "entry_date": {date}, "hours": {hours}, "unit": {"h"}})
	}

	legacy("2026-04-01", "2.1")

	e.post("/bases/"+itoa64(e.baseID64)+"/billing-rule", url.Values{"billing_increment_h": {"25"}, "minimum_h": {""}})
	if b, err := e.st.GetBase(e.ctx, e.baseID64); err != nil || b.HasBillingRule() {
		t.Fatalf("an increment above 24 h was stored: %+v, %v", b, err)
	}
	e.post("/bases/"+itoa64(e.baseID64)+"/billing-rule", url.Values{"billing_increment_h": {"0,25"}, "minimum_h": {"1"}})
	b, err := e.st.GetBase(e.ctx, e.baseID64)
	if err != nil || b.BillingIncrementH.String() != "0.25" || b.MinimumH.String() != "1" {
		t.Fatalf("rule not stored: %+v, %v", b, err)
	}
	if page := e.get("/bases"); !strings.Contains(page, "Takt 0,25 h, mind. 1 h") {
		t.Error("bases list does not show the billing rule")
	}
	form := e.get(neighborBookingURL(e.neighborID, e.yearID64))
	if !strings.Contains(form, `data-billing-increment="0.25"`) || !strings.Contains(form, "auf 0,25 h aufgerundet") {
		t.Error("booking form neither passes the rule to the preview nor explains it")
	}

	legacy("2026-04-02", "2.1")
	e.post("/entries", url.Values{"booking_form_version": {"2"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
		"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {"2026-04-03"}, "mode": {"gespann"},
		"gespann_id": {itoa64(e.gespannID)}, "hours": {"0.5"}})
	e.post("/entries", url.Values{"booking_form_version": {"2"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
		"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {"2026-04-04"}, "mode": {"free"},
		"partner_label": {"Leihgerät"}, "partner_rate": {"30"}, "hours": {"0.5"}})
	e.post("/entries/quick", url.Values{"year_id": {yid}, "neighbor_id": {nid},
		"q_date": {"2026-04-05"}, "q_gespann": {itoa64(e.gespannID)}, "q_hours": {"1.1"}})

	got := byDate()
	for date, want := range map[string]string{
		"2026-04-01": "2.1",  // stored before the rule: unchanged
		"2026-04-02": "2.25", // older form: rounded up to the quarter hour
		"2026-04-03": "1",    // unified form: minimum per job
		"2026-04-04": "0.5",  // agreed free-text rate: exact
		"2026-04-05": "1.25", // quick entry: rounded up
	} {
		en, ok := got[date]
		if !ok {
			t.Errorf("%s: booking missing", date)
			continue
		}
		if en.Hours.String() != want {
			t.Errorf("%s: billed %s h, want %s h", date, en.Hours, want)
		}
		if en.MachineLabels != "Leihgerät" && !en.Cost.Equal(en.Hours.Mul(en.HourlyRate).Round(2)) {
			t.Errorf("%s: cost %s does not follow billed hours × %s", date, en.Cost, en.HourlyRate)
		}
	}

	// The booking stored before the rule changes only through the reviewed
	// recalc: the preview shows its billed hours, and applying writes them.
	preview := e.get("/neighbors/" + nid + "/recalc?year=" + yid)
	if !strings.Contains(preview, `<span class="old">2,1 h</span>`) || !strings.Contains(preview, "<strong>2,25 h</strong>") {
		t.Error("recalc preview does not show the billed hours of the older booking")
	}
	e.post("/neighbors/"+nid+"/recalc", url.Values{"year_id": {yid}})
	if old := byDate()["2026-04-01"]; old.Hours.String() != "2.25" || !old.Quantity.Equal(old.Hours) ||
		!old.Cost.Equal(old.Hours.Mul(old.HourlyRate).Round(2)) {
		t.Errorf("recalc did not bill the older booking: %s h, quantity %s, cost %s", old.Hours, old.Quantity, old.Cost)
	}
	if again := byDate()["2026-04-02"]; again.Hours.String() != "2.25" {
		t.Errorf("recalc changed an already billed booking to %s h", again.Hours)
	}

	// A locked basis refuses the change and keeps its rule.
	if err := e.st.SetBaseLocked(e.ctx, e.baseID64, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.st.SetBaseLocked(e.ctx, e.baseID64, false) })
	if body := e.post("/bases/"+itoa64(e.baseID64)+"/billing-rule", url.Values{"billing_increment_h": {"0"}, "minimum_h": {"0"}}); !strings.Contains(body, "gesperrt") {
		t.Error("a locked basis did not refuse the rule change")
	}
	if b, err := e.st.GetBase(e.ctx, e.baseID64); err != nil || !b.HasBillingRule() {
		t.Fatalf("a locked basis lost its rule: %+v, %v", b, err)
	}
}
