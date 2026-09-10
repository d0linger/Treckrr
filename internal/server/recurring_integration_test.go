//go:build integration

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

func TestRecurringNextRunValidationIntegration(t *testing.T) {
	e := newItEnv(t)
	entryID, err := e.st.CreateEntry(e.ctx, &models.Entry{
		NeighborID: e.neighborID, BillingYearID: e.yearID64, Date: time.Now(),
		Unit: "ha", Quantity: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(20),
		TaskLabel: "Date validation",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, input, wantDate string
		reject                bool
	}{
		{name: "valid date", input: "2099-02-03", wantDate: "2099-02-03"},
		{name: "raw boundary", input: strings.Repeat(" ", maxNameLen-10) + "2099-02-03", wantDate: "2099-02-03"},
		{name: "over boundary", input: strings.Repeat("x", maxNameLen+1), reject: true},
		{name: "whitespace padding", input: strings.Repeat(" ", maxNameLen) + "2099-02-03", reject: true},
		{name: "unicode padding", input: strings.Repeat("\u2003", maxNameLen) + "2099-02-03", reject: true},
		{name: "empty keeps default"},
		{name: "invalid keeps default", input: "not-a-date"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := e.st.ListRecurring(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			known := make(map[int64]bool, len(before))
			for _, rule := range before {
				known[rule.ID] = true
			}
			defaultMin := time.Now().AddDate(0, 0, 7).Format(time.DateOnly)
			body := e.post(fmt.Sprintf("/entries/%d/recur", entryID), url.Values{
				"interval_kind": {"monthly"}, "next_run": {tc.input},
			})
			defaultMax := time.Now().AddDate(0, 0, 7).Format(time.DateOnly)
			after, err := e.st.ListRecurring(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if tc.reject {
				if !strings.Contains(body, "Startdatum darf höchstens 100 Zeichen lang sein.") {
					t.Error("missing length validation message")
				}
				if len(after) != len(before) {
					t.Fatal("rejected date created a recurring rule")
				}
				return
			}
			if len(after) != len(before)+1 || !strings.Contains(body, "Serie eingerichtet.") {
				t.Fatal("accepted date did not create exactly one recurring rule")
			}
			for _, rule := range after {
				if known[rule.ID] {
					continue
				}
				gotDate := rule.NextRun.Format(time.DateOnly)
				if tc.wantDate != "" && gotDate != tc.wantDate {
					t.Errorf("next run = %s, want %s", gotDate, tc.wantDate)
				}
				if tc.wantDate == "" && gotDate != defaultMin && gotDate != defaultMax {
					t.Errorf("default next run = %s, want %s or %s", gotDate, defaultMin, defaultMax)
				}
				if rule.NeighborID != e.neighborID || rule.IntervalKind != "monthly" {
					t.Fatal("created rule lost its neighbor or cadence")
				}

				// Editing must enforce the same raw limit and leave the existing
				// schedule untouched when validation fails.
				updatePath := fmt.Sprintf("/recurring/%d/update", rule.ID)
				body = e.post(updatePath, url.Values{
					"interval_kind": {"weekly"},
					"next_run":      {strings.Repeat(" ", maxNameLen) + "2099-04-05"},
				})
				if !strings.Contains(body, "Startdatum darf höchstens 100 Zeichen lang sein.") {
					t.Error("update missing length validation message")
				}
				unchanged, err := e.st.ListRecurring(e.ctx)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, updated := range unchanged {
					if updated.ID != rule.ID {
						continue
					}
					found = true
					if !updated.NextRun.Equal(rule.NextRun) || updated.IntervalKind != rule.IntervalKind {
						t.Fatal("rejected update changed the schedule")
					}
				}
				if !found {
					t.Fatal("rejected update removed the rule")
				}
				return
			}
			t.Fatal("new recurring rule not found")
		})
	}
}
