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
	t.Parallel()
	for _, tc := range []struct {
		name, action string
		copy         bool
	}{
		{name: "edit", action: "/entries/7/update"},
		{name: "copy", action: "/entries", copy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			personID := int64(9)
			page := execPage(t, "entry_edit", map[string]any{
				"Entry": models.Entry{ID: 7, Unit: models.UnitMannstunde, PersonID: &personID,
					Quantity: decimal.RequireFromString("2.5"), UnitPrice: decimal.RequireFromString("23.75")},
				"Neighbor": models.Neighbor{ID: 3}, "Year": models.BillingYear{ID: 42},
				"Persons": []models.Person{{ID: personID, Name: "Hans <Alt>", Archived: true}},
				"Copy":    tc.copy, "PairPartnerLabel": "Traktor",
			})
			for _, want := range []string{
				`action="` + tc.action + `"`, `name="booking_kind" value="labor"`,
				`name="booking_direction" value="out"`, `name="person_id" required`,
				`value="9" selected>Hans &lt;Alt&gt; (archiviert)`,
				`name="hours"`, `value="2.5"`, `name="person_rate"`, `value="23.75"`,
			} {
				if !strings.Contains(page, want) {
					t.Errorf("labor field contract missing %s", want)
				}
			}
			for _, forbidden := range []string{`data-entry-form`, `name="quantity"`, `name="gespann_id"`, `name="sync_pair" value="1" checked`} {
				if strings.Contains(page, forbidden) {
					t.Errorf("labor form contains conflicting field/default %s", forbidden)
				}
			}
			if tc.copy && (!strings.Contains(page, `name="neighbor_id" value="3"`) || !strings.Contains(page, `name="year_id" value="42"`)) {
				t.Fatal("labor copy lost the owning account")
			}
		})
	}
}

// TestEntryPairSyncIsExplicit preserves independently recorded person hours by
// requiring a fresh opt-in before editing the linked machine/person booking.
func TestEntryPairSyncIsExplicit(t *testing.T) {
	t.Parallel()
	page := execPage(t, "entry_edit", map[string]any{
		"Entry": models.Entry{ID: 7, Unit: "h"}, "PairPartnerLabel": "Mannstunden Hans",
	})
	if !strings.Contains(page, `name="sync_pair" value="1">`) || strings.Contains(page, `name="sync_pair" value="1" checked`) {
		t.Fatal("linked hours must be available only through explicit opt-in")
	}
	if !strings.Contains(page, "Ohne Auswahl bleiben die Stunden der verknüpften Buchung unverändert.") {
		t.Fatal("pair-sync consequences are not explained")
	}
}

// TestLegacyLaborWithoutPersonRemainsEditable preserves old hand-entered labor
// that has no person attribution instead of requiring a newly invented person.
func TestLegacyLaborWithoutPersonRemainsEditable(t *testing.T) {
	t.Parallel()
	page := execPage(t, "entry_edit", map[string]any{
		"Entry": models.Entry{ID: 7, Unit: models.UnitMannstunde}, "UnitIsCustom": true, "IsQtyEntry": true,
	})
	if !strings.Contains(page, `data-entry-form`) || !strings.Contains(page, `name="quantity"`) || strings.Contains(page, `name="person_id" required`) {
		t.Fatal("legacy unattributed labor must remain editable as a quantity entry")
	}
}

// TestMachineCopyExplainsSinglePosition avoids promising an implicit helper
// copy when the established copy endpoint creates only the selected entry.
func TestMachineCopyExplainsSinglePosition(t *testing.T) {
	t.Parallel()
	page := execPage(t, "entry_edit", map[string]any{
		"Entry": models.Entry{ID: 7, Unit: "h"}, "Copy": true,
	})
	if !strings.Contains(page, "Kopiert wird nur diese Position. Verknüpfte Mannstunden werden nicht automatisch mitkopiert.") {
		t.Fatal("machine copy does not explain that linked labor is not copied")
	}
}
