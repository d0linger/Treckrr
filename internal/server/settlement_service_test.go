package server

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestSettlementTotals(t *testing.T) {
	tests := []struct {
		name              string
		cost              string
		ledger            []models.LedgerEntry
		payments          []models.Payment
		expectedLedger    string
		expectedSaldo     string
		expectedPaid      string
		expectedRemaining string
	}{
		{
			name: "receivable with counterclaim and partial payment",
			cost: "100.00",
			ledger: []models.LedgerEntry{
				{Amount: decimal.RequireFromString("-20.00")},
				{Amount: decimal.RequireFromString("999.00"), Voided: true},
			},
			payments:          []models.Payment{{Amount: decimal.RequireFromString("30.00")}},
			expectedLedger:    "-20.00",
			expectedSaldo:     "80.00",
			expectedPaid:      "30.00",
			expectedRemaining: "50.00",
		},
		{
			name: "overpayment is a credit",
			cost: "40.00",
			payments: []models.Payment{
				{Amount: decimal.RequireFromString("50.00")},
				{Amount: decimal.RequireFromString("-5.00")},
			},
			expectedLedger:    "0.00",
			expectedSaldo:     "40.00",
			expectedPaid:      "45.00",
			expectedRemaining: "-5.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ledger, saldo, paid, remaining := settlementTotals(
				decimal.RequireFromString(tt.cost),
				tt.ledger,
				tt.payments,
			)
			if ledger.StringFixed(2) != tt.expectedLedger || saldo.StringFixed(2) != tt.expectedSaldo ||
				paid.StringFixed(2) != tt.expectedPaid || remaining.StringFixed(2) != tt.expectedRemaining {
				t.Fatalf("totals = ledger %s, saldo %s, paid %s, remaining %s; want %s, %s, %s, %s",
					ledger, saldo, paid, remaining,
					tt.expectedLedger, tt.expectedSaldo, tt.expectedPaid, tt.expectedRemaining)
			}
		})
	}
}
