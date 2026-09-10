package main

import (
	"testing"
	"time"
)

// Business records expire by event year; security noise uses its anniversary.
func TestAuditRetentionCutoffsCalendar(t *testing.T) {
	// A reference "now" on a leap day, chosen so the 7-year window (2021..2028)
	// contains two leap years (2024, 2028) — the case where a fixed 365-day
	// duration drifts the most.
	now := time.Date(2028, time.February, 29, 12, 0, 0, 0, time.UTC)

	short, long := auditRetentionCutoffs(now)

	// Short window: 1 calendar year back from 2028-02-29 is 2027-02-28 (2027 is not
	// a leap year, so Feb 29 does not exist and Go normalises to Mar 1).
	wantShort := time.Date(2027, time.March, 1, 12, 0, 0, 0, time.UTC)
	if !short.Equal(wantShort) {
		t.Errorf("short cutoff = %s, want %s (1 calendar year back)", short, wantShort)
	}

	// Every event in 2021 remains retained throughout 2028.
	wantLong := time.Date(2021, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !long.Equal(wantLong) {
		t.Errorf("long cutoff = %s, want %s (7 calendar years back)", long, wantLong)
	}

	// A fixed-duration cutoff would delete part of the retained event year.
	naiveLong := now.Add(-7 * 365 * 24 * time.Hour)
	if !long.Before(naiveLong) {
		t.Errorf("calendar long cutoff %s should retain longer (be earlier) than naive fixed-duration cutoff %s", long, naiveLong)
	}
}

// A non-leap reference date must also retain the complete business event year.
func TestAuditRetentionCutoffsNonLeap(t *testing.T) {
	now := time.Date(2030, time.June, 15, 8, 30, 0, 0, time.UTC)
	short, long := auditRetentionCutoffs(now)

	if want := time.Date(2029, time.June, 15, 8, 30, 0, 0, time.UTC); !short.Equal(want) {
		t.Errorf("short cutoff = %s, want %s", short, want)
	}
	if want := time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC); !long.Equal(want) {
		t.Errorf("long cutoff = %s, want %s", long, want)
	}
	// Long window is always older than the short window.
	if !long.Before(short) {
		t.Errorf("long cutoff %s should be before short cutoff %s", long, short)
	}
}

func TestAuditRetentionYearBoundary(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC),
		time.Date(2026, time.December, 31, 23, 59, 59, 0, time.UTC),
		time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2028, time.February, 29, 12, 0, 0, 0, time.FixedZone("operator", 3600)),
	} {
		t.Run(now.Format(time.RFC3339), func(t *testing.T) {
			_, long := auditRetentionCutoffs(now)
			want := time.Date(now.Year()-7, time.January, 1, 0, 0, 0, 0, now.Location())
			if !long.Equal(want) {
				t.Errorf("long = %v, want %v", long, want)
			}
			if long.Location() != now.Location() {
				t.Error("operator time zone lost")
			}
		})
	}
}
