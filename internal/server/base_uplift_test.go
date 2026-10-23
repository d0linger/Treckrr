package server

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestUpliftRate raises or lowers a rate by a percentage and rounds to the
// chosen step, half away from zero.
func TestUpliftRate(t *testing.T) {
	t.Parallel()
	d := decimal.RequireFromString
	for _, tc := range []struct{ rate, pct, step, want string }{
		{"0.36", "4", "0.01", "0.37"},
		{"0.36", "4", "0.0001", "0.3744"},
		{"6", "4.5", "0.05", "6.25"},
		{"6", "4.5", "0.01", "6.27"},
		{"5", "-10", "0.1", "4.5"},
		{"0.33", "0", "0.01", "0.33"},
	} {
		if got := upliftRate(d(tc.rate), d(tc.pct), d(tc.step)); !got.Equal(d(tc.want)) {
			t.Errorf("upliftRate(%s, %s%%, %s) = %s, want %s", tc.rate, tc.pct, tc.step, got, tc.want)
		}
	}
}
