package web

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestYearsKeepsOnePrimaryActionPerRow prevents administration controls from
// competing with the next workflow action on the year list.
func TestYearsKeepsOnePrimaryActionPerRow(t *testing.T) {
	t.Parallel()
	page := mainContent(t, execPage(t, "years", map[string]any{
		"Rows": []map[string]any{
			{"ID": int64(1), "Year": 2025, "Base": "2025 — Standard", "Entries": 3, "Completed": true},
			{"ID": int64(2), "Year": 2026, "Base": "2025 — Standard", "Entries": 4},
		},
		"Bases":    []models.PriceBase{{ID: 1, Year: 2025, Name: "Standard"}},
		"HasBases": true,
	}))
	if count := strings.Count(page, `class="btn btn--primary btn--sm"`); count != 2 {
		t.Errorf("year-row primary actions = %d, want 2", count)
	}
	for _, want := range []string{"Jahreswechsel", "Abschluss prüfen", "Weitere Aktionen", "Jahresdaten bearbeiten"} {
		if !strings.Contains(page, want) {
			t.Errorf("year list missing %q", want)
		}
	}
}

// TestYearClosingPrioritizesOpenChecks keeps unresolved items above the final
// action while relegating the complete checklist to one optional disclosure.
func TestYearClosingPrioritizesOpenChecks(t *testing.T) {
	t.Parallel()
	page := mainContent(t, execPage(t, "year_closing", map[string]any{
		"Year": map[string]any{"ID": int64(2), "Year": 2026, "Status": "in_progress"},
		"Checks": []store.ClosingCheck{
			{Key: "clean", Label: "Belege", Detail: "Vollständig"},
			{Key: "open", Label: "Zahlungen", Detail: "Beträge prüfen", Count: 2, Amount: decimal.NewFromInt(80)},
		},
		"OpenChecks": 1,
	}))
	for _, want := range []string{"1 Punkt(e) offen", "Zahlungen", "Alle Prüfungen anzeigen", "Jahr 2026 abschließen"} {
		if !strings.Contains(page, want) {
			t.Errorf("closing view missing %q", want)
		}
	}
	if strings.Contains(page, `class="list__row card"`) {
		t.Error("closing checklist still renders every check as a separate card")
	}
}
