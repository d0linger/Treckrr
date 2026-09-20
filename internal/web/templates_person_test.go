package web

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestNeighborPersonFieldsRender(t *testing.T) {
	for _, tc := range []struct {
		name    string
		persons []models.Person
	}{
		{name: "no_helpers"},
		{name: "priced_and_unpriced_helpers", persons: []models.Person{
			{ID: 1, Name: "Hans", HourlyRate: decimal.NewFromInt(20)},
			{ID: 2, Name: "Ohne <Satz>", HourlyRate: decimal.Zero},
			{ID: 3, Name: "Negativ", HourlyRate: decimal.NewFromInt(-5)},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page := execPage(t, "neighbor", map[string]any{
				"Base": models.PriceBase{ID: 1}, "Year": models.BillingYear{ID: 1, Year: 2026},
				"Neighbor": models.Neighbor{ID: 1, Name: "Testhof"},
				"Gespanne": []models.Gespann{{ID: 1, Name: "Testgespann"}},
				"Persons":  tc.persons, "Entries": []models.Entry{},
				"Saldo": decimal.Zero, "TotalHours": decimal.Zero,
				"PaidSum": decimal.Zero, "Remaining": decimal.Zero,
			})
			wantPersons := 0
			if len(tc.persons) > 0 {
				wantPersons = 6
				for _, want := range []string{`value="1">Hans`, `value="2" disabled>Ohne &lt;Satz&gt;`, `value="3" disabled>Negativ`} {
					if !strings.Contains(page, want) {
						t.Errorf("missing priced/disabled/escaped option %q", want)
					}
				}
			}
			if got := strings.Count(page, `name="q_person"`); got != wantPersons {
				t.Errorf("person fields = %d, want %d", got, wantPersons)
			}
			for _, field := range []string{"q_date", "q_gespann", "q_hours", "q_key"} {
				if got := strings.Count(page, `name="`+field+`"`); got != 6 {
					t.Errorf("%s fields = %d, want 6 aligned rows", field, got)
				}
			}
		})
	}
}

func TestEntryEditRecurringPersonOption(t *testing.T) {
	for _, tc := range []struct {
		name, label  string
		copy, voided bool
		want         bool
	}{
		{name: "linked_helper", label: "Mannstunden Hans", want: true},
		{name: "no_eligible_helper"},
		{name: "copy", label: "Mannstunden Hans", copy: true},
		{name: "voided", label: "Mannstunden Hans", voided: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			people := []models.BookingPerson(nil)
			if tc.label != "" {
				people = []models.BookingPerson{{ID: 2, Name: strings.TrimPrefix(tc.label, "Mannstunden "), Hours: decimal.NewFromInt(1), Rate: decimal.NewFromInt(20)}}
			}
			page := execPage(t, "entry_edit", map[string]any{
				"Entry":         models.Entry{ID: 1, Unit: "h", Date: time.Now(), Voided: tc.voided},
				"BookingValues": map[string]string{"booking_kind": "equipment", "booking_direction": "out"},
				"BookingPeople": people, "BookingCopy": tc.copy, "BookingVoided": tc.voided,
				"BookingHasOptionalPeople": tc.want,
				"RecurringAction": func() string {
					if tc.want {
						return "/entries/1/recur"
					}
					return ""
				}(),
			})
			if got := strings.Contains(page, `name="with_person" value="1" checked`); got != tc.want {
				t.Errorf("recurring helper option present = %v, want %v", got, tc.want)
			}
		})
	}
}
