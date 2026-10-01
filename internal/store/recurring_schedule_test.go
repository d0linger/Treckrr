package store

import (
	"testing"
	"time"
)

// TestRecurringPreviewUsesRunnerCadence keeps the UI preview and materializer on
// the same monthly-clamping and inclusive-end semantics.
func TestRecurringPreviewUsesRunnerCadence(t *testing.T) {
	start := time.Date(2026, time.January, 31, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.March, 28, 0, 0, 0, 0, time.UTC)
	got := recurringPreview(start, "monthly", &end, 6)
	want := []string{"2026-01-31", "2026-02-28", "2026-03-28"}
	if len(got) != len(want) {
		t.Fatalf("preview length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Format("2006-01-02") != want[i] {
			t.Errorf("preview[%d] = %s, want %s", i, got[i].Format("2006-01-02"), want[i])
		}
	}
}
