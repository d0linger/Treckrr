package server

import "testing"

func TestEquipmentIsHourly(t *testing.T) {
	for _, unit := range []string{"h", "H", "Std", "Std.", " Stunde ", "Stunden"} {
		if !equipmentIsHourly(unit) {
			t.Errorf("equipmentIsHourly(%q) = false", unit)
		}
	}
	for _, unit := range []string{"Füllung", "Fuhre", "m³", ""} {
		if equipmentIsHourly(unit) {
			t.Errorf("equipmentIsHourly(%q) = true", unit)
		}
	}
}

func TestBoundedEquipmentDecimal(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		positive bool
		valid    bool
	}{
		{raw: "1000", positive: true, valid: true},
		{raw: "12,3456", valid: true},
		{raw: "0", valid: true},
		{raw: "0", positive: true},
		{raw: "-1"},
		{raw: "1.00001"},
		{raw: "1e4"},
		{raw: "1000000000"},
	} {
		_, valid := boundedEquipmentDecimal(tc.raw, tc.positive)
		if valid != tc.valid {
			t.Errorf("boundedEquipmentDecimal(%q, %t) = %t, want %t", tc.raw, tc.positive, valid, tc.valid)
		}
	}
}
