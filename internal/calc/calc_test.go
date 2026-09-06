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
			rate := GespannRate(&tc.tractor, &tc.load, tc.machines)
			got := Cost(dec(tc.hours), rate)
			if got.StringFixed(2) != tc.want {
				t.Fatalf("cost = %s, want %s (rate %s)", got.StringFixed(2), tc.want, rate)
			}
		})
	}
}

// TestGespannRateWithoutTractor covers the machines-only rig: work where the
// customer supplies the tractor and only the implement is billed. The tractor and
// its load level price each other (PS × €/PS), so they are all-or-nothing — half
// a pair contributes nothing rather than silently pricing the other half.
func TestGespannRateWithoutTractor(t *testing.T) {
	tr := models.Tractor{PS: decimal.RequireFromString("130")}
	ll := models.LoadLevel{CostPerPS: decimal.RequireFromString("0.36")}
	// 1,7 m × 6,471 €/AB·h = 11,00 €/h — the ÖKL concrete-mixer case.
	mixer := models.Machine{
		WorkingWidth: decimal.RequireFromString("1.7"),
		CostPerAB:    decimal.RequireFromString("6.471"),
	}
	machines := []models.Machine{mixer}

	cases := []struct {
		name string
		t    *models.Tractor
		l    *models.LoadLevel
		ms   []models.Machine
		want string
	}{
		{"machines only", nil, nil, machines, "11.00"},
		{"tractor and machines", &tr, &ll, machines, "57.80"}, // 46,80 + 11,00
		{"tractor only", &tr, &ll, nil, "46.80"},
		{"nothing at all", nil, nil, nil, "0.00"},
		// Half a pair has no rate to compute; it must not fall back to pricing the
		// machines as if the tractor had been left out deliberately.
		{"tractor without load level", &tr, nil, machines, "11.00"},
		{"load level without tractor", nil, &ll, machines, "11.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GespannRate(tc.t, tc.l, tc.ms); got.StringFixed(2) != tc.want {
				t.Errorf("GespannRate = %s, want %s", got.StringFixed(2), tc.want)
			}
		})
	}
}

// TestGespannRateMachinesOnlyIsExactlyTheMachineSum guards the property the
// billing depends on: without a tractor the rate is the machine sum to the cent,
// with no stray rounding from the absent tractor term.
func TestGespannRateMachinesOnlyIsExactlyTheMachineSum(t *testing.T) {
	widths := []string{"1.2", "1.5", "1.7", "2.3", "3.8", "6.8"}
	costs := []string{"6.471", "9.8", "11.2", "14.5", "18.9", "22"}
	for _, w := range widths {
		for _, c := range costs {
			m := models.Machine{
				WorkingWidth: decimal.RequireFromString(w),
				CostPerAB:    decimal.RequireFromString(c),
			}
			want := MachineRate(m)
			if got := GespannRate(nil, nil, []models.Machine{m}); !got.Equal(want) {
				t.Errorf("%s m × %s: GespannRate = %s, MachineRate = %s", w, c, got, want)
			}
			// Two of them: the sum must not drift either.
			want2 := MachineRate(m).Add(MachineRate(m))
			if got := GespannRate(nil, nil, []models.Machine{m, m}); !got.Equal(want2) {
				t.Errorf("%s m × %s twice: GespannRate = %s, want %s", w, c, got, want2)
			}
		}
	}
}

// A half-set tractor pair is not a machines-only rig. The rate function already
// contributes nothing for one, but the callers used to hand it through as if the
// tractor had been left out deliberately, so the rig list advertised the machine
// sum for a combination the booking path refuses. This pins the rate side; the
// caller side is covered in the server package.
func TestGespannRateHalfPairIsNotMachinesOnly(t *testing.T) {
	tr := models.Tractor{PS: decimal.RequireFromString("130")}
	ll := models.LoadLevel{CostPerPS: decimal.RequireFromString("0.36")}
	m := models.Machine{
		WorkingWidth: decimal.RequireFromString("2.5"),
		CostPerAB:    decimal.RequireFromString("16.4"),
	}
	full := GespannRate(&tr, &ll, []models.Machine{m})
	half := GespannRate(&tr, nil, []models.Machine{m})
	if full.StringFixed(2) != "87.80" { // 46,80 + 41,00
		t.Fatalf("complete rig = %s, want 87.80", full.StringFixed(2))
	}
	// The tractor silently vanishing is exactly the trap: the number looks
	// plausible, so a caller must not treat it as a price.
	if half.StringFixed(2) != "41.00" {
		t.Fatalf("half pair = %s, want 41.00 (the machine sum)", half.StringFixed(2))
	}
	if half.Equal(full) {
		t.Error("half pair and complete rig priced the same")
	}
}
