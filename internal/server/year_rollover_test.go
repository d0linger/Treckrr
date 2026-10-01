package server

import (
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

// TestRecurringRuleStatusCountsIgnoresInactiveErrors keeps historical errors
// on paused or completed rules out of the rollover's blocked count.
func TestRecurringRuleStatusCountsIgnoresInactiveErrors(t *testing.T) {
	active, blocked := recurringRuleStatusCounts([]models.RecurringEntry{
		{Active: true},
		{Active: true, LastError: "wartet"},
		{Active: false, LastError: "historic error"},
	})
	if active != 2 || blocked != 1 {
		t.Fatalf("counts = active %d, blocked %d; want 2, 1", active, blocked)
	}
}
