package web

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestBookingListSourceIsolation guards overlapping IDs: only outgoing entries
// can show bulk checkboxes or booking-photo links; transfers retain their owner.
func TestBookingListSourceIsolation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		completed bool
	}{
		{name: "open"},
		{name: "completed", completed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rows := []store.BookingRow{
				{EntryRow: store.EntryRow{Entry: models.Entry{ID: 7, NeighborID: 3, TaskLabel: "Eigene Arbeit", Unit: "h"}}, Source: "entry", Direction: "out", Kind: "equipment"},
				{EntryRow: store.EntryRow{Entry: models.Entry{ID: 7, NeighborID: 3, TaskLabel: "Nachbars Arbeit", Unit: "h"}}, Source: "ledger", Direction: "in", Kind: "equipment"},
				{EntryRow: store.EntryRow{Entry: models.Entry{ID: 8, NeighborID: 3, TaskLabel: "Vortrag"}}, Source: "ledger", Direction: "out", Kind: "transfer"},
			}
			page := execPage(t, "entries", map[string]any{
				"Year": models.BillingYear{ID: 42, Year: 2026}, "Filter": map[string]string{},
				"Rows": rows, "SumCost": decimal.Zero, "Total": 3, "Pages": 1, "Page": 1,
				"PhotoCounts": map[int64]int{7: 2}, "HasEntryRows": true, "Completed": tc.completed,
			})
			wantCheckboxes := 2
			if tc.completed {
				wantCheckboxes = 0
			}
			if got := strings.Count(page, `name="booking_id"`); got != wantCheckboxes {
				t.Errorf("selectable rows = %d, want %d", got, wantCheckboxes)
			}
			if got := strings.Count(page, `title="Belegfotos"`); got != 1 {
				t.Errorf("photo links = %d, want one on the entry only", got)
			}
			for _, want := range []string{`href="/entries/7/edit">Eigene Arbeit`, `href="/ledger/7/edit">Nachbars Arbeit`, `href="/neighbors/3?year=42">Vortrag`, "Jahresübertrag", "Ich schulde", "Nachbar schuldet"} {
				if !strings.Contains(page, want) {
					t.Errorf("source-specific navigation/label missing: %s", want)
				}
			}
			if strings.Contains(page, `href="/ledger/8/edit"`) {
				t.Fatal("carry-forward exposes an independent edit destination")
			}
		})
	}
}
