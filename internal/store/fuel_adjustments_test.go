package store

import (
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

func TestEffectiveFuelAdjustment(t *testing.T) {
	adjustments := []models.FuelAdjustment{
		{ID: 3, EffectiveFrom: adjustmentDay(2026, 7, 1), Label: "Sommer"},
		{ID: 1, EffectiveFrom: adjustmentDay(2026, 1, 1), Label: "Winter"},
		{ID: 2, EffectiveFrom: adjustmentDay(2026, 4, 1), Label: "Frühling"},
	}
	tests := []struct {
		name string
		date time.Time
		want string
	}{
		{name: "before first", date: adjustmentDay(2025, 12, 31)},
		{name: "first boundary", date: adjustmentDay(2026, 1, 1), want: "Winter"},
		{name: "between versions", date: adjustmentDay(2026, 6, 30), want: "Frühling"},
		{name: "latest boundary", date: adjustmentDay(2026, 7, 1), want: "Sommer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EffectiveFuelAdjustment(adjustments, test.date)
			if test.want == "" {
				if got != nil {
					t.Fatalf("got %q, want no adjustment", got.Label)
				}
				return
			}
			if got == nil || got.Label != test.want {
				t.Fatalf("got %#v, want %q", got, test.want)
			}
		})
	}
}

func adjustmentDay(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}
