package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestEntryFromTemplatePreservesFrozenPricingSnapshot(t *testing.T) {
	tmpl := models.RecurTemplate{
		TaskLabel: "Mähen",
		Unit:      "h", Hours: decimal.RequireFromString("2.345"),
		HourlyRate: decimal.RequireFromString("46.00"),
		Cost:       decimal.RequireFromString("107.87"),
		MachineIDs: []int64{11, 12},
	}

	entry := entryFromTemplate(tmpl)
	if entry.TaskLabel != tmpl.TaskLabel || entry.Unit != tmpl.Unit {
		t.Fatalf("entry labels = %q/%q, want %q/%q", entry.TaskLabel, entry.Unit, tmpl.TaskLabel, tmpl.Unit)
	}
	if !entry.Hours.Equal(tmpl.Hours) || !entry.HourlyRate.Equal(tmpl.HourlyRate) || !entry.Cost.Equal(tmpl.Cost) {
		t.Fatalf("entry snapshot = %s × %s = %s, want %s × %s = %s",
			entry.Hours, entry.HourlyRate, entry.Cost, tmpl.Hours, tmpl.HourlyRate, tmpl.Cost)
	}
	if entry.Cost.StringFixed(2) != "107.87" {
		t.Fatalf("cost = %s, want frozen 107.87", entry.Cost.StringFixed(2))
	}
}
