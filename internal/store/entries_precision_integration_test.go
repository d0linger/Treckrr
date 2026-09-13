//go:build integration

package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestSyncPairHoursPrecision ensures direct callers cannot split machine hours,
// quantity and cost by rounding just the numeric(10,3) column.
func TestSyncPairHoursPrecision(t *testing.T) {
	st, _, yearID, neighborID := scratchBookingFixture(t)
	ctx := t.Context()
	id, err := st.CreateEntry(ctx, &models.Entry{NeighborID: neighborID, BillingYearID: yearID,
		Date: time.Now(), Hours: dec("2"), HourlyRate: dec("46"), Cost: dec("92")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, hours := range []string{"1.2345", "10000000", "0", "-1"} {
		if _, err := st.SyncPairHours(ctx, id, dec(hours)); !errors.Is(err, store.ErrPairHoursPrecision) {
			t.Fatalf("sync %s error=%v", hours, err)
		}
		entry, err := st.GetEntry(ctx, id)
		if err != nil || !entry.Hours.Equal(dec("2")) || !entry.Quantity.Equal(dec("2")) || !entry.Cost.Equal(dec("92")) {
			t.Fatalf("rejected synchronization modified entry: %+v %v", entry, err)
		}
	}
	if _, err := st.SyncPairHours(ctx, id, dec("1.2340")); err != nil {
		t.Fatal(err)
	}
	entry, err := st.GetEntry(ctx, id)
	if err != nil || !entry.Hours.Equal(entry.Quantity) || !entry.Cost.Equal(entry.Hours.Mul(entry.HourlyRate).Round(2)) {
		t.Fatalf("accepted synchronization is inconsistent: %+v %v", entry, err)
	}
}
