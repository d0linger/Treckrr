package web

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestLaborEditAndCopyRetainAttribution guards the dedicated labor field
// contract so editing/copying cannot silently turn a helper into generic units.
func TestLaborEditAndCopyRetainAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		copy         bool
	}{
		{name: "edit", action: "/entries/7/update"},
		{name: "copy", action: "/entries", copy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			personID := int64(9)
			page := execPage(t, "entry_edit", map[string]any{
				"Entry":    models.Entry{ID: 7, Unit: models.UnitMannstunde},
				"Neighbor": models.Neighbor{ID: 3}, "Year": models.BillingYear{ID: 42}, "Base": models.PriceBase{ID: 1},
				"Persons":       []models.Person{{ID: personID, Name: "Hans <Alt>", Archived: true}},
				"BookingValues": map[string]string{"booking_kind": "labor", "booking_direction": "out", "hours": "2.5"},
				"BookingPeople": []models.BookingPerson{{ID: 7, PersonID: &personID, Name: "Hans <Alt>", Hours: decimal.RequireFromString("2.5"), Rate: decimal.RequireFromString("23.75")}},
				"BookingAction": tc.action, "BookingEdit": !tc.copy, "BookingCopy": tc.copy,
			})
			for _, want := range []string{
				`action="` + tc.action + `"`, `name="booking_kind"`,
				`name="booking_direction" value="out"`, `name="person_id"`,
				`value="9" data-person-rate="0" selected>Hans &lt;Alt&gt; (archiviert)`,
				`name="hours"`, `value="2.5"`, `name="person_rate"`, `value="23.75"`,
			} {
				if !strings.Contains(page, want) {
					t.Errorf("labor field contract missing %s", want)
				}
			}
			kindValue := `name="booking_kind" value="labor"`
			if tc.copy {
				kindValue = `option value="labor" selected`
			}
			if !strings.Contains(page, kindValue) {
				t.Errorf("labor kind contract missing %s", kindValue)
			}
			for _, forbidden := range []string{`name="sync_pair" value="1" checked`} {
				if strings.Contains(page, forbidden) {
					t.Errorf("labor form contains conflicting field/default %s", forbidden)
				}
			}
			if tc.copy && !strings.Contains(page, `name="copy_people" value="1" checked`) {
				t.Fatal("labor copy no longer includes its people by explicit default")
			}
		})
	}
}

// TestEntryPairSyncIsExplicit preserves independently recorded person hours by
// requiring a fresh opt-in before editing the linked machine/person booking.
func TestEntryPairSyncIsExplicit(t *testing.T) {
	page := execPage(t, "entry_edit", map[string]any{
		"Entry": models.Entry{ID: 7, Unit: "h"}, "Base": models.PriceBase{ID: 1},
		"BookingValues": map[string]string{"booking_kind": "equipment", "booking_direction": "out"},
		"BookingPeople": []models.BookingPerson{{ID: 8, Name: "Hans", Hours: decimal.NewFromInt(2), Rate: decimal.NewFromInt(25)}},
		"BookingEdit":   true,
	})
	if !strings.Contains(page, `name="person_row_id" value="8"`) || !strings.Contains(page, `name="person_hours"`) {
		t.Fatal("linked person is not available as an explicit component row")
	}
}

// TestLegacyLaborWithoutPersonRemainsEditable preserves old hand-entered labor
// that has no person attribution instead of requiring a newly invented person.
func TestLegacyLaborWithoutPersonRemainsEditable(t *testing.T) {
	page := execPage(t, "entry_edit", map[string]any{
		"Entry": models.Entry{ID: 7, Unit: models.UnitMannstunde}, "Base": models.PriceBase{ID: 1},
		"BookingValues": map[string]string{"booking_kind": "labor", "booking_direction": "out", "hours": "1"},
		"BookingPeople": []models.BookingPerson{{ID: 7, Name: "Nicht zugeordnet", Hours: decimal.NewFromInt(1), Rate: decimal.NewFromInt(20)}},
		"BookingEdit":   true,
	})
	if !strings.Contains(page, `data-unified-booking`) || !strings.Contains(page, `value="Nicht zugeordnet"`) {
		t.Fatal("legacy unattributed labor must remain editable with an explicit free-name snapshot")
	}
}

// TestMachineCopyExplainsSinglePosition avoids promising an implicit helper
// copy when the established copy endpoint creates only the selected entry.
func TestMachineCopyExplainsSinglePosition(t *testing.T) {
	page := execPage(t, "entry_edit", map[string]any{
		"Entry": models.Entry{ID: 7, Unit: "h"}, "Base": models.PriceBase{ID: 1},
		"BookingValues": map[string]string{"booking_kind": "equipment", "booking_direction": "out"},
		"BookingPeople": []models.BookingPerson{{Name: "Hans", Hours: decimal.NewFromInt(1), Rate: decimal.NewFromInt(20)}},
		"BookingCopy":   true,
	})
	if !strings.Contains(page, `name="copy_people" value="1" checked`) || !strings.Contains(page, "Personen mitkopieren") {
		t.Fatal("complete booking copy does not expose its person-copy choice")
	}
}
