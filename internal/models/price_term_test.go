package models

import (
	"testing"
	"time"
)

// TestPriceTerm pins the standing price term: the validity range is inclusive
// on both ends, the newest covering start wins, the discount rounds to the cent
// and reports what it took off, and the line note names the term.
func TestPriceTerm(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	end := day("2026-04-30")
	terms := []PriceTerm{
		{ID: 1, Label: "Familienrabatt", DiscountPct: dec("10"), ValidFrom: day("2026-01-01")},
		{ID: 2, Label: "Aktion", DiscountPct: dec("15"), ValidFrom: day("2026-04-01"), ValidTo: &end},
	}
	for date, want := range map[string]string{
		"2025-12-31": "",
		"2026-01-01": "Familienrabatt",
		"2026-04-01": "Aktion",
		"2026-04-30": "Aktion",
		"2026-05-01": "Familienrabatt",
	} {
		got := ""
		if term := EffectivePriceTerm(terms, day(date)); term != nil {
			got = term.Label
		}
		if got != want {
			t.Errorf("%s: effective term %q, want %q", date, got, want)
		}
	}

	rate, perH := terms[0].Discounted(dec("46"))
	if !rate.Equal(dec("41.4")) || !perH.Equal(dec("4.6")) {
		t.Errorf("10 %% off 46 = %s (−%s), want 41.40 (−4.60)", rate, perH)
	}
	rate, perH = PriceTerm{DiscountPct: dec("7.5")}.Discounted(dec("33.33"))
	if !rate.Equal(dec("30.83")) || !rate.Add(perH).Equal(dec("33.33")) {
		t.Errorf("7.5 %% off 33.33 = %s (−%s), want 30.83 and a remainder summing to the list rate", rate, perH)
	}

	if got := PriceTermNote("Familienrabatt", dec("7.50")); got != "abzgl. 7,5 % Familienrabatt" {
		t.Errorf("note = %q", got)
	}
	if got := PriceTermNote("", dec("0")); got != "" {
		t.Errorf("note without a term = %q, want empty", got)
	}
}
