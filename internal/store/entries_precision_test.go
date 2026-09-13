package store

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestMachineHoursRepresentable covers exact storage precision and range.
func TestMachineHoursRepresentable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, hours string
		want        bool
	}{
		{"whole", "2", true}, {"three places", "1.234", true}, {"trailing zero", "1.2340", true},
		{"four places", "1.2345", false}, {"rounding up", "1.9999", false},
		{"maximum", "9999999.999", true}, {"overflow", "10000000", false},
		{"zero", "0", false}, {"negative", "-1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MachineHoursRepresentable(decimal.RequireFromString(tc.hours)); got != tc.want {
				t.Fatalf("hours=%s representable=%t, want %t", tc.hours, got, tc.want)
			}
		})
	}
}
