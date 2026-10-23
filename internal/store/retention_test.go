package store

import (
	"testing"
	"time"
)

// TestRetentionCutoff counts full calendar years: records from 2018 are kept
// through 2025 and may go in 2026, whatever the day.
func TestRetentionCutoff(t *testing.T) {
	t.Parallel()
	for _, now := range []time.Time{
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 22, 15, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC),
	} {
		if got := RetentionCutoff(now, 7); !got.Equal(time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("RetentionCutoff(%s, 7) = %s, want 2019-01-01", now, got)
		}
	}
}
