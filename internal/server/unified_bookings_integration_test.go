//go:build integration

package server

import (
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestUnifiedBookingDatesIntegration rejects invalid unified dates on create,
// offline replay and edit without writing rows or changing existing bookings.
func TestUnifiedBookingDatesIntegration(t *testing.T) {
	e := newItEnv(t)
	for _, kind := range []string{"equipment", "quantity"} {
		t.Run(kind, func(t *testing.T) {
			form := url.Values{
				"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)},
				"booking_kind": {kind}, "booking_direction": {"out"}, "task_label": {"Date guard " + kind},
				"unit": {"h"}, "gespann_id": {itoa64(e.gespannID)}, "hours": {"2"},
			}
			if kind == "quantity" {
				form.Set("unit", "ha")
				form.Set("quantity", "2")
				form.Set("unit_price", "30")
			}
			before, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
			if err != nil {
				t.Fatal(err)
			}
			for _, date := range []string{"", "not-a-date", "2026-02-30"} {
				form.Set("entry_date", date)
				body := e.post("/entries", form)
				if !strings.Contains(html.UnescapeString(body), "Bitte ein gültiges Datum angeben.") {
					t.Fatalf("online create did not reject date %q", date)
				}
				// Replays need a permanent validation error, not a successful redirect.
				form.Set("csrf_token", e.csrf("/"))
				req, err := http.NewRequest(http.MethodPost, e.srv.URL+"/entries", strings.NewReader(form.Encode()))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("X-Offline-Replay", "1")
				resp, err := e.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				b, readErr := io.ReadAll(resp.Body)
				resp.Body.Close()
				if readErr != nil {
					t.Fatal(readErr)
				}
				if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(b), "Bitte ein gültiges Datum angeben.") {
					t.Fatalf("replay date %q: status=%d body=%s", date, resp.StatusCode, b)
				}
			}
			after, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
			if err != nil || len(after) != len(before) {
				t.Fatalf("invalid dates wrote rows: before=%d after=%d err=%v", len(before), len(after), err)
			}

			form.Set("entry_date", "2026-09-13")
			e.post("/entries", form)
			entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
			if err != nil || len(entries) != len(before)+1 {
				t.Fatalf("valid create failed: entries=%d err=%v", len(entries), err)
			}
			var id int64
			for _, entry := range entries {
				if entry.TaskLabel == form.Get("task_label") {
					id = entry.ID
					if entry.Date.Format("2006-01-02") != "2026-09-13" {
						t.Fatalf("valid date not preserved: %v", entry.Date)
					}
				}
			}
			if id == 0 {
				t.Fatal("created entry missing")
			}
			original, err := e.st.GetEntry(e.ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/entries/%d/update", id)
			form.Set("task_label", "Must not change on rejection")
			for _, date := range []string{"", "not-a-date", "2026-02-30"} {
				form.Set("entry_date", date)
				if body := e.post(path, form); !strings.Contains(html.UnescapeString(body), "Bitte ein gültiges Datum angeben.") {
					t.Fatalf("edit did not reject date %q", date)
				}
				got, err := e.st.GetEntry(e.ctx, id)
				if err != nil || !got.Date.Equal(original.Date) || got.TaskLabel != original.TaskLabel || !got.Cost.Equal(original.Cost) {
					t.Fatalf("invalid edit changed booking: %+v err=%v", got, err)
				}
			}
			form.Set("entry_date", "2026-09-14")
			e.post(path, form)
			got, err := e.st.GetEntry(e.ctx, id)
			if err != nil || got.Date.Format("2006-01-02") != "2026-09-14" || got.TaskLabel != form.Get("task_label") {
				t.Fatalf("valid edit failed: %+v err=%v", got, err)
			}
		})
	}
}

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
	incoming.Set("task_label", "")
	incoming.Set("booking_form_version", "2")
	incoming.Set("mode", "gespann")
	incoming.Set("gespann_id", itoa64(e.gespannID))
	incoming.Set("hours", "2")
	incoming.Set("person_row_id", "0")
	incoming.Set("person_id", itoa64(pid))
	incoming.Set("person_name", "")
	incoming.Set("person_hours", "3.5")
	incoming.Set("person_rate", "")
	incoming.Set("person_state", "active")
	incoming.Set("idempotency_key", "unified-http-"+e.uname)
	e.post("/entries", incoming)
	e.post("/entries", incoming)
	ledger, err := e.st.ListNeighborLedger(e.ctx, e.yearID64, e.neighborID)
	if err != nil || len(ledger) != 1 || ledger[0].Amount.StringFixed(2) != "-162.00" {
		t.Fatalf("incoming=%+v %v", ledger, err)
	}
	snapshot := ledger[0].Booking
	if snapshot == nil || snapshot.TaskLabel != "IT-Gespann" || snapshot.Mode != "gespann" || snapshot.GespannID == nil || *snapshot.GespannID != e.gespannID ||
		len(snapshot.MachineIDs) != 1 || snapshot.MachineIDs[0] != e.machineID || snapshot.PartnerLabel != "IT-Gespann" ||
		snapshot.UnitPrice.String() != "46" || len(snapshot.People) != 1 || snapshot.People[0].Name != "Unified helper "+e.uname ||
		snapshot.People[0].PersonID == nil || *snapshot.People[0].PersonID != pid || snapshot.People[0].Rate.String() != "20" {
		t.Fatalf("incoming shared-pool snapshot=%+v", snapshot)
	}
	if entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64); err != nil || len(entries) != 0 {
		t.Fatalf("counterclaim entered outgoing work: %d %v", len(entries), err)
	}
	beleg := html.UnescapeString(e.get(fmt.Sprintf("/neighbors/%d/beleg?year=%d&grundlage=1", e.neighborID, e.yearID64)))
	if strings.Contains(beleg, "Ich schulde") || strings.Contains(beleg, "IT-Gespann · IT-Gespann") {
		t.Fatalf("incoming equipment is redundantly described on Beleg")
	}
	if !strings.Contains(beleg, `<div class="beleg__gm"><span>IT-Maschine</span>`) {
		t.Fatalf("incoming catalog machine missing from Beleg cost basis")
	}
	incoming.Set("hours", "3")
	incoming.Set("person_row_id", itoa64(snapshot.People[0].ID))
	e.post(fmt.Sprintf("/ledger/%d/update", ledger[0].ID), incoming)
	_, _, updated, err := e.st.GetLedgerEntry(e.ctx, ledger[0].ID)
	if err != nil || updated.Booking == nil || updated.Amount.StringFixed(2) != "-208.00" {
		t.Fatalf("structured edit=%+v %v", updated, err)
	}
	labor := base("labor", "out")
	labor.Set("task_label", "")
	labor.Set("booking_form_version", "2")
	labor.Set("person_row_id", "0")
	labor.Set("person_id", itoa64(pid))
	labor.Set("person_name", "")
	labor.Set("hours", "1.5")
	labor.Set("person_hours", "")
	labor.Set("person_rate", "25")
	labor.Set("person_state", "active")
	labor.Set("idempotency_key", "unified-own-"+e.uname)
	e.post("/entries", labor)
	e.post("/entries", labor)
	entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 1 || entries[0].TaskLabel != "Mannstunden Unified helper "+e.uname || entries[0].Cost.StringFixed(2) != "37.50" || entries[0].PersonID == nil || *entries[0].PersonID != pid {
		t.Fatalf("own labor=%+v %v", entries, err)
	}
	labor.Set("hours", "2")
	e.post("/entries", labor)
	if retry, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64); err != nil || len(retry) != 1 || retry[0].Cost.StringFixed(2) != "37.50" {
		t.Fatalf("changed retry created/repriced labor: %+v %v", retry, err)
	}
	labor.Del("booking_form_version")
	e.post(fmt.Sprintf("/entries/%d/update", entries[0].ID), labor)
	changed, err := e.st.GetEntry(e.ctx, entries[0].ID)
	if err != nil || changed.Cost.StringFixed(2) != "50.00" || changed.PersonID == nil || *changed.PersonID != pid {
		t.Fatalf("labor edit=%+v %v", changed, err)
	}
	machine := base("equipment", "out")
	machine.Set("task_label", "")
	machine.Set("booking_form_version", "2")
	machine.Set("mode", "gespann")
	machine.Set("unit", "h")
	machine.Set("gespann_id", itoa64(e.gespannID))
	machine.Set("hours", "2")
	machine.Set("person_row_id", "0")
	machine.Set("person_id", itoa64(pid))
	machine.Set("person_name", "")
	machine.Set("person_hours", "3.5")
	machine.Set("person_rate", "22")
	machine.Set("person_state", "active")
	e.post("/entries", machine)
	entries, err = e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(entries) != 3 {
		t.Fatalf("machine pair rows=%d %v", len(entries), err)
	}
	found, foundMachine := false, false
	for _, entry := range entries {
		if entry.LinkedEntryID != nil {
			found = entry.Quantity.String() == "3.5" && entry.Cost.StringFixed(2) == "77.00"
		} else if entry.GespannID != nil {
			foundMachine = entry.TaskLabel == "IT-Gespann"
		}
	}
	if !found || !foundMachine {
		t.Fatal("derived equipment label or independent helper hours/rate not preserved")
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
