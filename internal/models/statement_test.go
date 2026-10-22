package models

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestFeedbackKeyBindsEveryDisplayedLineValue(t *testing.T) {
	line := InvoiceLine{Date: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), Label: "Mähen", Unit: "ha",
		Quantity: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(25), Cost: decimal.NewFromInt(50)}
	for _, change := range []func(*InvoiceLine){
		func(l *InvoiceLine) { l.Date = l.Date.AddDate(0, 0, 1) },
		func(l *InvoiceLine) { l.Label += " geändert" },
		func(l *InvoiceLine) { l.Unit = "h" },
		func(l *InvoiceLine) { l.Quantity = l.Quantity.Add(decimal.NewFromInt(1)) },
		func(l *InvoiceLine) { l.UnitPrice = l.UnitPrice.Add(decimal.NewFromInt(1)) },
		func(l *InvoiceLine) { l.Cost = l.Cost.Add(decimal.NewFromInt(1)) },
	} {
		updated := line
		change(&updated)
		if updated.FeedbackKey() == line.FeedbackKey() {
			t.Fatal("changed line kept its selection fingerprint")
		}
	}
	line.Date = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	other := line
	other.Label = "Other legacy date"
	if line.FeedbackKey() == other.FeedbackKey() {
		t.Fatal("unsupported JSON date collapsed distinct lines")
	}
}
