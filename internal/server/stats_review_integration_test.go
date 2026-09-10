//go:build integration

package server

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestStatsUnlabeledTaskHasNoFalseDrilldownIntegration(t *testing.T) {
	e := newItEnv(t)
	for _, label := range []string{"", "Sonstige"} {
		entry := models.Entry{
			NeighborID: e.neighborID, BillingYearID: e.yearID64,
			Date: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), TaskLabel: label,
			Hours: decimal.NewFromInt(1), HourlyRate: decimal.NewFromInt(10), Cost: decimal.NewFromInt(10),
		}
		if _, err := e.st.CreateEntry(e.ctx, &entry, nil); err != nil {
			t.Fatal(err)
		}
	}
	page := e.get(fmt.Sprintf("/stats?year=%d", e.yearID64))
	if !strings.Contains(page, "Ohne Tätigkeit") {
		t.Error("unlabeled bookings missing from chart")
	}
	if strings.Contains(page, "task=Ohne") {
		t.Error("synthetic label links to a nonexistent task")
	}
	if !strings.Contains(page, "task=Sonstige") {
		t.Error("real task named Sonstige lost its drilldown")
	}
}
