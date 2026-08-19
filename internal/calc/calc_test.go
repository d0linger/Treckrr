package calc

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// TestDaysBetween covers the plain case and the DST boundary: Austria springs
// forward on 2026-03-29 (02:00→03:00), so the calendar day 28→29 spans only 23h.
// A plain hours/24 truncation would return 0 for that 1-day gap; DaysBetween
// rounds and returns 1. Autumn's 25h day (2026-10-25) is the symmetric case.
func TestDaysBetween(t *testing.T) {
	vienna, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	d := func(y int, m time.Month, day int) time.Time {
		return time.Date(y, m, day, 12, 0, 0, 0, vienna) // noon, so the time-of-day never matters
	}
	cases := []struct {
		name     string
		from, to time.Time
		want     int
	}{
		{"same day", d(2026, 6, 1), d(2026, 6, 1), 0},
		{"one day forward", d(2026, 6, 1), d(2026, 6, 2), 1},
		{"one day back", d(2026, 6, 2), d(2026, 6, 1), -1},
		{"a week", d(2026, 6, 1), d(2026, 6, 8), 7},
		{"DST spring-forward day (23h)", d(2026, 3, 28), d(2026, 3, 29), 1},
		{"across the whole DST switch", d(2026, 3, 27), d(2026, 3, 30), 3},
		{"DST fall-back day (25h)", d(2026, 10, 24), d(2026, 10, 25), 1},
	}
	for _, c := range cases {
		if got := DaysBetween(c.from, c.to); got != c.want {
			t.Errorf("%s: DaysBetween = %d, want %d", c.name, got, c.want)
		}
	}
}

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
