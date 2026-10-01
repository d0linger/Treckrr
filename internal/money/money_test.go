package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestAmount pins decimal multiplication and cent rounding independently from
// any one booking model or HTTP workflow.
func TestAmount(t *testing.T) {
	tests := []struct {
		name          string
		quantity      string
		unitPrice     string
		expectedTotal string
	}{
		{name: "whole values", quantity: "2", unitPrice: "46", expectedTotal: "92.00"},
		{name: "fractional hours", quantity: "2.345", unitPrice: "46", expectedTotal: "107.87"},
		{name: "round half up", quantity: "1", unitPrice: "10.005", expectedTotal: "10.01"},
		{name: "negative account line", quantity: "-2.345", unitPrice: "46", expectedTotal: "-107.87"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quantity := decimal.RequireFromString(test.quantity)
			unitPrice := decimal.RequireFromString(test.unitPrice)
			if got := Amount(quantity, unitPrice).StringFixed(2); got != test.expectedTotal {
				t.Fatalf("Amount(%s, %s) = %s, want %s", quantity, unitPrice, got, test.expectedTotal)
			}
		})
	}
}
