package calc

import (
	"testing"

	"treckrr/internal/models"
)

// Exact-value coverage for the individual rate functions, pinning the 2-decimal
// rounding so the money->decimal migration can be shown to preserve every value.

func TestTractorRateExact(t *testing.T) {
	cases := []struct {
		name          string
		ps, costPerPS float64
		want          float64
	}{
		{"simple", 100, 0.50, 50.00},
		{"zero ps", 0, 1.23, 0.00},
		{"rounds up at .005", 3, 0.335, 1.01},      // 1.005 -> 1.01
		{"rounds down below .005", 3, 0.334, 1.00}, // 1.002 -> 1.00
		{"spreadsheet leicht 50PS", 50, 0.33, 16.50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := TractorRate(models.Tractor{PS: c.ps}, models.LoadLevel{CostPerPS: c.costPerPS})
			if got != c.want {
				t.Fatalf("TractorRate(%g,%g) = %g, want %g", c.ps, c.costPerPS, got, c.want)
			}
		})
	}
}

func TestMachineRateExact(t *testing.T) {
	cases := []struct {
		name        string
		width, cost float64
		want        float64
	}{
		{"simple", 3.0, 10.0, 30.00},
		{"rounds", 2.5, 4.011, 10.03}, // 10.0275 -> 10.03
		{"zero width", 0, 99, 0.00},
		{"spreadsheet Frontmähwerk", 3.06, 12, 36.72},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := MachineRate(models.Machine{WorkingWidth: c.width, CostPerAB: c.cost})
			if got != c.want {
				t.Fatalf("MachineRate(%g,%g) = %g, want %g", c.width, c.cost, got, c.want)
			}
		})
	}
}

func TestGespannRateExact(t *testing.T) {
	tr := models.Tractor{PS: 120}
	ll := models.LoadLevel{CostPerPS: 0.40} // 48.00
	machines := []models.Machine{
		{WorkingWidth: 3.0, CostPerAB: 5.0}, // 15.00
		{WorkingWidth: 2.0, CostPerAB: 7.5}, // 15.00
	}
	if got := GespannRate(tr, ll, machines); got != 78.00 {
		t.Fatalf("GespannRate = %g, want 78.00", got)
	}
	if got := GespannRate(tr, ll, nil); got != 48.00 {
		t.Fatalf("GespannRate(no machines) = %g, want 48.00", got)
	}
}

func TestCostExact(t *testing.T) {
	cases := []struct{ hours, rate, want float64 }{
		{2.0, 50.0, 100.00},
		{1.5, 78.0, 117.00},
		{0.25, 33.33, 8.33}, // 8.3325 -> 8.33
		{0, 100, 0.00},
	}
	for _, c := range cases {
		if got := Cost(c.hours, c.rate); got != c.want {
			t.Fatalf("Cost(%g,%g) = %g, want %g", c.hours, c.rate, got, c.want)
		}
	}
}
