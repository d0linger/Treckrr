package calc

import (
	"testing"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestGespannRateAndCost(t *testing.T) {
	// Values taken directly from the source spreadsheet "Noppanschoftshilfe.xlsx".
	loads := map[string]models.LoadLevel{
		"leicht": {CostPerPS: dec("0.33")},
		"mittel": {CostPerPS: dec("0.36")},
		"schwer": {CostPerPS: dec("0.38")},
	}
	machines := map[string]models.Machine{
		"Heckmähwerk":  {WorkingWidth: dec("2.4"), CostPerAB: dec("10")},
		"Frontmähwerk": {WorkingWidth: dec("3.06"), CostPerAB: dec("12")},
		"Schwader":     {WorkingWidth: dec("3.8"), CostPerAB: dec("5")},
		"Fräse":        {WorkingWidth: dec("2.0"), CostPerAB: dec("18")},
	}

	cases := []struct {
		name     string
		tractor  models.Tractor
		load     models.LoadLevel
		machines []models.Machine
		hours    string
		want     string
	}{
		{
			name:     "Mähen 4095 mittel + Heck + Front, 2.25h",
			tractor:  models.Tractor{PS: dec("100")},
			load:     loads["mittel"],
			machines: []models.Machine{machines["Heckmähwerk"], machines["Frontmähwerk"]},
			hours:    "2.25",
			want:     "217.62",
		},
		{
			name:     "Schwadern 948 leicht + Schwader, 4h",
			tractor:  models.Tractor{PS: dec("50")},
			load:     loads["leicht"],
			machines: []models.Machine{machines["Schwader"]},
			hours:    "4",
			want:     "142.00",
		},
		{
			name:     "Fräsen 9083 schwer + Fräse, 3h",
			tractor:  models.Tractor{PS: dec("94")},
			load:     loads["schwer"],
			machines: []models.Machine{machines["Fräse"]},
			hours:    "3",
			want:     "215.16",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rate := GespannRate(tc.tractor, tc.load, tc.machines)
			got := Cost(dec(tc.hours), rate)
			if got.StringFixed(2) != tc.want {
				t.Fatalf("cost = %s, want %s (rate %s)", got.StringFixed(2), tc.want, rate)
			}
		})
	}
}
