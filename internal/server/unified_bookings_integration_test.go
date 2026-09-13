//go:build integration

package server

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"
)

// TestUnifiedBookingsIntegration exercises real handler create/edit/copy contracts
// across incoming services, own labor and a machine with independent helper hours.
func TestUnifiedBookingsIntegration(t *testing.T) {
	e := newItEnv(t)
	pid, err := e.st.CreatePerson(e.ctx, "Unified helper "+e.uname, decimal.NewFromInt(20), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(e.ctx, `DELETE FROM persons WHERE id=$1`, pid) })
	base := func(kind, direction string) url.Values {
		return url.Values{"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)}, "entry_date": {"2026-09-13"}, "task_label": {"Ernte"}, "booking_kind": {kind}, "booking_direction": {direction}}
	}
	incoming := base("equipment", "in")
	incoming.Set("hours", "2")
	incoming.Set("partner_label", "Nachbars Gespann")
	incoming.Set("partner_rate", "50")
	incoming.Set("partner_person", "Franz")
	incoming.Set("partner_person_rate", "20")
	incoming.Set("partner_person_hours", "3.5")
	incoming.Set("idempotency_key", "unified-http-"+e.uname)
	e.post("/entries", incoming)
	e.post("/entries", incoming)
	ledger, err := e.st.ListNeighborLedger(e.ctx, e.yearID64, e.neighborID)
	if err != nil || len(ledger) != 1 || ledger[0].Amount.StringFixed(2) != "-170.00" {
		t.Fatalf("incoming=%+v %v", ledger, err)
	}
	if entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64); err != nil || len(entries) != 0 {
		t.Fatalf("counterclaim entered outgoing work: %d %v", len(entries), err)
	}
	incoming.Set("hours", "3")
	e.post(fmt.Sprintf("/ledger/%d/update", ledger[0].ID), incoming)
	_, _, updated, err := e.st.GetLedgerEntry(e.ctx, ledger[0].ID)
	if err != nil || updated.Booking == nil || updated.Amount.StringFixed(2) != "-220.00" {
		t.Fatalf("structured edit=%+v %v", updated, err)
	}
	labor := base("labor", "out")
	labor.Set("person_id", itoa64(pid))
	labor.Set("hours", "1.5")
	labor.Set("person_rate", "25")
	labor.Set("idempotency_key", "unified-own-"+e.uname)
	e.post("/entries", labor)
	e.post("/entries", labor)
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 || entries[0].Cost.StringFixed(2) != "37.50" || entries[0].PersonID == nil || *entries[0].PersonID != pid {
		t.Fatalf("own labor=%+v %v", entries, err)
	}
	labor.Set("hours", "2")
	e.post("/entries", labor)
	if retry, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64); err != nil || len(retry) != 1 || retry[0].Cost.StringFixed(2) != "37.50" {
		t.Fatalf("changed retry created/repriced labor: %+v %v", retry, err)
	}
	e.post(fmt.Sprintf("/entries/%d/update", entries[0].ID), labor)
	changed, err := e.st.GetEntry(e.ctx, entries[0].ID)
	if err != nil || changed.Cost.StringFixed(2) != "50.00" || changed.PersonID == nil || *changed.PersonID != pid {
		t.Fatalf("labor edit=%+v %v", changed, err)
	}
	machine := base("equipment", "out")
	machine.Set("unit", "h")
	machine.Set("gespann_id", itoa64(e.gespannID))
	machine.Set("hours", "2")
	machine.Set("person_id", itoa64(pid))
	machine.Set("person_hours", "3.5")
	machine.Set("person_rate", "22")
	e.post("/entries", machine)
	entries, err = e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 3 {
		t.Fatalf("machine pair rows=%d %v", len(entries), err)
	}
	found := false
	for _, entry := range entries {
		if entry.LinkedEntryID != nil {
			found = entry.Quantity.String() == "3.5" && entry.Cost.StringFixed(2) == "77.00"
		}
	}
	if !found {
		t.Fatal("independent helper hours/rate not preserved")
	}
	for _, helper := range entries {
		if helper.LinkedEntryID == nil {
			continue
		}
		labor.Set("hours", "1.2345")
		labor.Set("person_rate", "22")
		labor.Set("sync_pair", "1")
		e.post(fmt.Sprintf("/entries/%d/update", helper.ID), labor)
		unchanged, err := e.st.GetEntry(e.ctx, helper.ID)
		if err != nil || !unchanged.Quantity.Equal(helper.Quantity) || !unchanged.Cost.Equal(helper.Cost) || unchanged.TaskLabel != helper.TaskLabel {
			t.Fatalf("invalid precision saved helper before rejecting pair: %+v %v", unchanged, err)
		}
		rig, err := e.st.GetEntry(e.ctx, *helper.LinkedEntryID)
		if err != nil || rig.Hours.String() != "2" || rig.Cost.StringFixed(2) != "92.00" {
			t.Fatalf("invalid precision modified machine: %+v %v", rig, err)
		}
		labor.Set("hours", "1.2340")
		e.post(fmt.Sprintf("/entries/%d/update", helper.ID), labor)
		rig, err = e.st.GetEntry(e.ctx, *helper.LinkedEntryID)
		if err != nil || !rig.Hours.Equal(rig.Quantity) || rig.Cost.StringFixed(2) != "56.76" {
			t.Fatalf("representable synchronization failed: %+v %v", rig, err)
		}
	}
}
