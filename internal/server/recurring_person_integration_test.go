package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestRecurringPersonSelectionIntegration(t *testing.T) {
	e := newItEnv(t)
	pid, err := e.st.CreatePerson(e.ctx, "Serienhelfer "+e.uname, decimal.NewFromInt(30), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM persons WHERE id=$1`, pid)
	})
	for _, tc := range []struct {
		name, unit                   string
		selected, voided, attributed bool
		wantOption, wantCompanion    bool
	}{
		{name: "selected", unit: "h", selected: true, wantOption: true, wantCompanion: true},
		{name: "unchecked", unit: "h", wantOption: true},
		{name: "voided_helper", unit: "h", selected: true, voided: true},
		{name: "quantity_booking", unit: "ha", selected: true},
		{name: "helper_only_series", unit: models.UnitMannstunde, attributed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &models.Entry{
				NeighborID: e.neighborID, BillingYearID: e.yearID64, Date: time.Now(),
				Unit: tc.unit, Hours: decimal.NewFromInt(2), HourlyRate: decimal.NewFromInt(40),
				Quantity: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(20), TaskLabel: tc.name,
			}
			var id, companionID int64
			var err error
			if tc.attributed {
				entry.PersonID = &pid
				id, err = e.st.CreateEntry(e.ctx, entry, nil)
			} else {
				id, companionID, err = e.st.CreateEntryPair(e.ctx, entry, nil, &models.Entry{
					NeighborID: e.neighborID, BillingYearID: e.yearID64, Date: entry.Date,
					Unit: models.UnitMannstunde, Quantity: decimal.NewFromInt(2),
					UnitPrice: decimal.NewFromInt(20), PersonID: &pid, TaskLabel: "Mannstunden Helfer",
				})
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.voided {
				if err := e.st.SetEntryVoided(e.ctx, companionID, true, "test"); err != nil {
					t.Fatal(err)
				}
			}
			page := e.get(fmt.Sprintf("/entries/%d/edit", id))
			if got := strings.Contains(page, `name="with_person"`); got != tc.wantOption {
				t.Errorf("helper checkbox visible = %v, want %v", got, tc.wantOption)
			}
			form := url.Values{"interval_kind": {"weekly"}, "next_run": {"2099-01-01"}}
			if tc.selected {
				form.Set("with_person", "1")
			}
			e.post(fmt.Sprintf("/entries/%d/recur", id), form)
			rules, err := e.st.ListRecurring(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, rule := range rules {
				if rule.NeighborID != e.neighborID || rule.Template.TaskLabel != tc.name {
					continue
				}
				comp := rule.Template.Companion
				if (comp != nil) != tc.wantCompanion {
					t.Fatalf("stored companion = %+v, wanted present %v", comp, tc.wantCompanion)
				}
				if comp != nil && (comp.PersonID != pid || !comp.Rate.Equal(decimal.NewFromInt(20)) || comp.Name != "Serienhelfer "+e.uname) {
					t.Errorf("series did not retain the helper and booked rate: %+v", comp)
				}
				if tc.attributed && (rule.Template.PersonID == nil || *rule.Template.PersonID != pid) {
					t.Errorf("helper-only series lost its person attribution")
				}
				return
			}
			t.Fatal("series was not created")
		})
	}
}
