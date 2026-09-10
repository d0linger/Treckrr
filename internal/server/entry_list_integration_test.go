package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The bookings list with filter, sorting and paging, and the bulk actions on
// it (Ausbaukarte 63/64). The part that matters is what bulk MUST NOT do:
// touch bookings frozen by an invoice or belonging to a closed year.
func TestEntryListAndBulkIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	// Three bookings on distinct days, plus one with a distinctive task text.
	for _, d := range []string{"2026-09-01", "2026-09-02", "2026-09-03"} {
		e.post("/entries", url.Values{
			"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
			"gespann_id": {itoa64(e.gespannID)}, "entry_date": {d},
			"hours": {"1"}, "unit": {"h"},
		})
	}
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"entry_date": {"2026-09-04"}, "unit": {"Ballen"},
		"task_label": {"Ballenpressen Spezial"}, "quantity": {"10"}, "unit_price": {"3"},
	})

	listURL := fmt.Sprintf("/buchungen?year=%d", yid)
	page := e.get(listURL)
	if !strings.Contains(page, "4 Buchung(en)") {
		t.Fatalf("list does not report 4 bookings")
	}
	// 3 × 46,00 + 30,00 = 168,00
	if !strings.Contains(page, "168,00") {
		t.Errorf("filtered sum is missing (want 168,00)")
	}

	// Date range narrows to two.
	if page := e.get(listURL + "&from=2026-09-02&to=2026-09-03"); !strings.Contains(page, "2 Buchung(en)") {
		t.Errorf("date filter did not narrow to 2")
	}
	// Task text finds exactly the one booking.
	page = e.get(listURL + "&task=spezial")
	if !strings.Contains(page, "1 Buchung(en)") || !strings.Contains(page, "Ballenpressen Spezial") {
		t.Errorf("task filter did not find the single matching booking")
	}
	// Unit filter likewise.
	if page := e.get(listURL + "&unit=Ballen"); !strings.Contains(page, "1 Buchung(en)") {
		t.Errorf("unit filter did not narrow to 1")
	}
	// An unknown neighbor id yields nothing rather than everything.
	if page := e.get(listURL + "&neighbor_id=999999999"); !strings.Contains(page, "0 Buchung(en)") {
		t.Errorf("a foreign neighbor filter must yield no rows")
	}

	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 4 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	ids := url.Values{"year_id": {itoa64(yid)}, "action": {"void"}, "reason": {"Doppelerfassung"}}
	for _, en := range entries[:2] {
		ids.Add("entry_id", itoa64(en.ID))
	}
	e.post("/buchungen/bulk", ids)
	entries, _ = e.st.ListEntries(e.ctx, nid, yid)
	voided := 0
	for _, en := range entries {
		if en.Voided {
			voided++
			if en.VoidReason != "Doppelerfassung" {
				t.Errorf("bulk void lost the reason: %q", en.VoidReason)
			}
		}
	}
	if voided != 2 {
		t.Errorf("%d bookings voided, want 2", voided)
	}
	// The voided ones drop out of the sum but stay visible unless filtered away.
	if page := e.get(listURL + "&voided=hide"); !strings.Contains(page, "2 Buchung(en)") {
		t.Errorf("hiding voided bookings did not narrow to 2")
	}
	if page := e.get(listURL + "&voided=only"); !strings.Contains(page, "2 Buchung(en)") {
		t.Errorf("only-voided did not narrow to 2")
	}

	// Un-voiding works the same way.
	ids.Set("action", "unvoid")
	e.post("/buchungen/bulk", ids)
	entries, _ = e.st.ListEntries(e.ctx, nid, yid)
	for _, en := range entries {
		if en.Voided {
			t.Errorf("bulk unvoid left booking %d voided", en.ID)
		}
	}

	// Now freeze an invoice: every bulk action must skip these bookings and say so.
	e.post(fmt.Sprintf("/neighbors/%d/invoice", nid), url.Values{"year_id": {itoa64(yid)}})
	all := url.Values{"year_id": {itoa64(yid)}, "action": {"delete"}}
	for _, en := range entries {
		all.Add("entry_id", itoa64(en.ID))
	}
	body := e.post("/buchungen/bulk", all)
	if !strings.Contains(body, "gesperrt") {
		t.Errorf("bulk delete on frozen bookings did not report the lock")
	}
	if after, _ := e.st.ListEntries(e.ctx, nid, yid); len(after) != 4 {
		t.Fatalf("bulk delete removed %d frozen booking(s)", 4-len(after))
	}
}

// Sammel-Festschreibung with a per-neighbor selection (Ausbaukarte 70): only
// the ticked neighbors get an invoice, and an empty selection issues nothing.
func TestBatchIssueSelectionIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64

	second, err := e.st.CreateNeighbor(e.ctx, "Zweiter "+e.uname, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := e.st.UpdateNeighbor(e.ctx, second, "Zweiter "+e.uname, "",
		"Feldweg 2, 4710 Testdorf", "", "", "", nil); err != nil {
		t.Fatalf("address: %v", err)
	}
	if err := e.st.AddNeighborToYear(e.ctx, yid, second); err != nil {
		t.Fatalf("add to year: %v", err)
	}
	for _, n := range []int64{nid, second} {
		e.post("/entries", url.Values{
			"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(n)},
			"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-09-10"},
			"hours": {"1"}, "unit": {"h"},
		})
	}

	// Empty selection: nothing may be frozen — this is the one mistake that
	// costs a Storno per invoice to undo.
	e.post(fmt.Sprintf("/years/%d/issue-all", yid), url.Values{})
	for _, n := range []int64{nid, second} {
		if _, err := e.st.GetInvoice(e.ctx, yid, n); err == nil {
			t.Fatalf("an empty selection issued an invoice for %d", n)
		}
	}

	// Only the second neighbor.
	e.post(fmt.Sprintf("/years/%d/issue-all", yid), url.Values{"neighbor_id": {itoa64(second)}})
	if _, err := e.st.GetInvoice(e.ctx, yid, second); err != nil {
		t.Errorf("the selected neighbor got no invoice: %v", err)
	}
	if _, err := e.st.GetInvoice(e.ctx, yid, nid); err == nil {
		t.Errorf("an unselected neighbor was invoiced anyway")
	}
}

// A recurring rule can be re-timed and fired once for today (Ausbaukarte 68).
func TestRecurringEditAndRunNowIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-09-20"},
		"hours": {"1"}, "unit": {"h"},
	})
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	// Series from that booking, starting far in the future so the scheduled run
	// never interferes with what "run now" does.
	e.post(fmt.Sprintf("/entries/%d/recur", entries[0].ID), url.Values{
		"interval_kind": {"weekly"}, "next_run": {"2099-01-05"},
	})
	rules, err := e.st.ListRecurring(e.ctx)
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	var ruleID int64
	for _, r := range rules {
		if r.NeighborID == nid {
			ruleID = r.ID
		}
	}
	if ruleID == 0 {
		t.Fatalf("no rule was created")
	}
	t.Cleanup(func() {
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM recurring_entries WHERE id=$1`, ruleID)
	})

	// Re-time it: monthly, next run on a different date.
	e.post(fmt.Sprintf("/recurring/%d/update", ruleID), url.Values{
		"interval_kind": {"monthly"}, "next_run": {"2099-02-09"},
	})
	rules, _ = e.st.ListRecurring(e.ctx)
	for _, r := range rules {
		if r.ID != ruleID {
			continue
		}
		if r.IntervalKind != "monthly" || r.NextRun.Format("2006-01-02") != "2099-02-09" {
			t.Errorf("rule not re-timed: %s / %s", r.IntervalKind, r.NextRun.Format("2006-01-02"))
		}
	}

	// The itEnv year is a synthetic far-future one, so today has no open year
	// for this neighbor: "run now" must SAY that instead of doing nothing.
	body := e.post(fmt.Sprintf("/recurring/%d/run-now", ruleID), url.Values{})
	if !strings.Contains(body, "kein offenes Abrechnungsjahr") {
		t.Errorf("run-now without an open year for today did not report it")
	}
	if after, _ := e.st.ListEntries(e.ctx, nid, yid); len(after) != 1 {
		t.Errorf("run-now booked into a year it should not have (n=%d)", len(after))
	}

	// A paused rule refuses outright.
	e.post(fmt.Sprintf("/recurring/%d/toggle", ruleID), url.Values{})
	if body := e.post(fmt.Sprintf("/recurring/%d/run-now", ruleID), url.Values{}); !strings.Contains(body, "pausiert") {
		t.Errorf("run-now on a paused rule did not refuse")
	}
}
