package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestLedgerBookingValues rejects invalid domain combinations and signed inputs.
func TestLedgerBookingValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		kind     string
		incoming bool
		quantity string
		want     string
	}{
		{"counter equipment", "equipment", true, "2", "-80.00"},
		{"counter labor", "labor", true, "2", "-80.00"},
		{"counter quantity", "quantity", true, "2", "-80.00"},
		{"fixed charge", "fixed", false, "1", "40.00"},
		{"fixed payable", "fixed", true, "1", "-40.00"},
		{"outgoing equipment forbidden", "equipment", false, "2", ""},
		{"unknown kind", "other", true, "2", ""},
		{"negative quantity", "equipment", true, "-2", ""},
		{"zero quantity", "equipment", true, "0", ""},
		{"overflow", "equipment", true, "1000000000", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := LedgerBookingInput{Incoming: tt.incoming, Booking: models.LedgerBooking{Version: 1, Kind: tt.kind,
				Quantity: decimal.RequireFromString(tt.quantity), UnitPrice: decimal.NewFromInt(40)}}
			amount, _, err := ledgerBookingValues(in)
			if tt.want == "" {
				if err == nil {
					t.Fatal("accepted invalid booking")
				}
				return
			}
			if err != nil || amount.StringFixed(2) != tt.want {
				t.Fatalf("amount=%s error=%v, want %s", amount, err, tt.want)
			}
		})
	}
}

// TestRecurringCompanionIndependentHours keeps new absolute hours and old defaults.
func TestRecurringCompanionIndependentHours(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, override, want string }{{"legacy", "0", "2"}, {"independent", "3.5", "3.5"}} {
		t.Run(tc.name, func(t *testing.T) {
			c := &models.RecurCompanion{PersonID: 1, Name: "Helper", Rate: decimal.NewFromInt(20), Hours: decimal.RequireFromString(tc.override)}
			e := &models.Entry{Unit: "h", Hours: decimal.NewFromInt(2)}
			got := companionEntry(c, e)
			if got == nil || got.Quantity.String() != tc.want || !got.Cost.Equal(got.Quantity.Mul(c.Rate)) {
				t.Fatalf("incorrect independent companion: %#v", got)
			}
		})
	}
}
