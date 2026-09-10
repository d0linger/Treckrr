package store_test

import (
	"testing"

	"github.com/d0linger/treckrr/internal/store"
)

func TestJournalRowCountsForRevenue(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		row  store.JournalRow
		want bool
	}{
		{name: "issued credit", row: store.JournalRow{Kind: "gutschrift", Status: "issued"}, want: true},
		{name: "reversed credit", row: store.JournalRow{Kind: "gutschrift", Status: "canceled", HasReversal: true}, want: true},
		{name: "cascade canceled credit", row: store.JournalRow{Kind: "gutschrift", Status: "canceled"}},
		{name: "canceled invoice", row: store.JournalRow{Kind: "invoice", Status: "canceled"}, want: true},
		{name: "credit reversal", row: store.JournalRow{Kind: "storno", RefKind: "gutschrift", Status: "issued"}, want: true},
		{name: "advance reversal", row: store.JournalRow{Kind: "storno", RefKind: "anzahlung", Status: "issued"}},
		{name: "advance", row: store.JournalRow{Kind: "anzahlung", Status: "issued"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.CountsForRevenue(); got != tc.want {
				t.Fatalf("CountsForRevenue() = %v, want %v", got, tc.want)
			}
		})
	}
}
