package web

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestPricesSeparatesCatalogFromEditors keeps the everyday rate reference free
// of forms and limits edit mode to the explicitly selected catalog section.
func TestPricesSeparatesCatalogFromEditors(t *testing.T) {
	t.Parallel()
	data := map[string]any{
		"Base":            models.PriceBase{ID: 2, Year: 2026},
		"PriceSection":    "machines",
		"FuelAdjustments": []models.FuelAdjustment{},
		"Loads": []models.LoadLevel{{
			ID: 3, Name: "mittel", CostPerPS: decimal.RequireFromString("0.3"),
		}},
		"TractorViews": []map[string]any{{
			"Tractor": models.Tractor{ID: 4, Ident: "4095", PS: decimal.NewFromInt(100), Active: true},
			"Rates": []map[string]any{{
				"Load": models.LoadLevel{Name: "mittel"}, "Rate": decimal.NewFromInt(30),
			}},
		}},
		"MachineViews": []map[string]any{{
			"Machine": models.Machine{ID: 5, Name: "Schwader", WorkingWidth: decimal.NewFromInt(4), Active: true},
			"Rate":    decimal.NewFromInt(20),
		}},
	}

	overview := mainContent(t, execPage(t, "prices", data))
	for _, want := range []string{"Aktive Sätze", "4095", "Schwader", "Grundlage bearbeiten"} {
		if !strings.Contains(overview, want) {
			t.Errorf("catalog overview missing %q", want)
		}
	}
	if strings.Contains(overview, `<form`) {
		t.Error("catalog overview exposes edit forms")
	}

	data["EditMode"] = true
	editor := mainContent(t, execPage(t, "prices", data))
	if !strings.Contains(editor, `action="/prices/machines"`) {
		t.Error("machine editor is missing")
	}
	for _, unwanted := range []string{`action="/prices/tractors"`, `action="/prices/loadlevels"`, `id="fuel-adjustments"`} {
		if strings.Contains(editor, unwanted) {
			t.Errorf("machine editor also renders unrelated section %s", unwanted)
		}
	}
	if strings.Contains(editor, `<details class="disclosure form-details" open>`) {
		t.Error("machine cost calculation opens automatically")
	}
}

func mainContent(t *testing.T, page string) string {
	t.Helper()
	_, main, ok := strings.Cut(page, `<main class="main"`)
	if !ok {
		t.Fatal("rendered page has no main landmark")
	}
	main, _, _ = strings.Cut(main, "</main>")
	return main
}
