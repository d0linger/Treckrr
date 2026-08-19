package main

import (
	"testing"
	"time"
)

// TestAuditRetentionCutoffsCalendar pins the calendar-year semantics of the audit
// retention cutoffs: each cutoff must land on the true anniversary date, not a
// fixed 365-day multiple, so leap days in the window never cause an early purge.
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

	// Long window: 7 calendar years back from 2028-02-29 is 2021-02-29, which does
	// not exist, so Go normalises to 2021-03-01. The record's true 7-year
	// anniversary is honored to the calendar day.
	wantLong := time.Date(2021, time.March, 1, 12, 0, 0, 0, time.UTC)
	if !long.Equal(wantLong) {
		t.Errorf("long cutoff = %s, want %s (7 calendar years back)", long, wantLong)
	}

	// Regression guard against the old fixed-duration form. PurgeAuditLog deletes
	// rows with created_at < cutoff, so a LATER cutoff purges MORE. The naive
	// 365-day-per-year cutoff (2021-03-02) sits one day LATER than the calendar
	// cutoff (2021-03-01), because it under-counts the leap day (2024-02-29) inside
	// the window — meaning it would purge a 2021-03-01 record one day BEFORE its true
	// 7-year anniversary. The calendar cutoff must therefore be the earlier (more-
	// retaining) of the two.
	naiveLong := now.Add(-7 * 365 * 24 * time.Hour)
	if !long.Before(naiveLong) {
		t.Errorf("calendar long cutoff %s should retain longer (be earlier) than naive fixed-duration cutoff %s", long, naiveLong)
	}
	// The gap is exactly the leap day inside the [cutoff, now) window (2024-02-29);
	// the 2028-02-29 leap day is the reference date itself, not inside the window.
	if diff := naiveLong.Sub(long); diff != 24*time.Hour {
		t.Errorf("naive vs calendar long-cutoff gap = %s, want 24h (one leap day in window)", diff)
	}
}

// TestAuditRetentionCutoffsNonLeap sanity-checks the ordinary (non-leap) case: the
// cutoffs are simply the same month/day, N years earlier.
func TestAuditRetentionCutoffsNonLeap(t *testing.T) {
	now := time.Date(2030, time.June, 15, 8, 30, 0, 0, time.UTC)
	short, long := auditRetentionCutoffs(now)

	if want := time.Date(2029, time.June, 15, 8, 30, 0, 0, time.UTC); !short.Equal(want) {
		t.Errorf("short cutoff = %s, want %s", short, want)
	}
	if want := time.Date(2023, time.June, 15, 8, 30, 0, 0, time.UTC); !long.Equal(want) {
		t.Errorf("long cutoff = %s, want %s", long, want)
	}
	// Long window is always older than the short window.
	if !long.Before(short) {
		t.Errorf("long cutoff %s should be before short cutoff %s", long, short)
	}
}
