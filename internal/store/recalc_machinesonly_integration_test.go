package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// A machines-only booking — the customer supplies the tractor, only the implement
// is billed — carries no tractor id. RecalcPreview used to reprice only bookings
// that had one, so these would have frozen at the rate they were booked at while
// every other booking followed the basis: change the implement's price and the
// preview would report "nothing to do" for them.
//
// The numbers are the ÖKL concrete-mixer case that prompted this: 1,7 m ×
// 6,471 €/AB·h = 11,00 €/h, raised to 1,7 × 7,0588 = 12,00 €/h.
func TestRecalcMachinesOnlyIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	yr := 4900 + os.Getpid()%1000
	name := fmt.Sprintf("MachOnly-Nachbar %d", os.Getpid())
	purgeFixtures(t, ctx, pool, fixtures{Years: []int{yr}, NeighborNames: []string{name}})

	baseID, err := st.CreateEmptyBase(ctx, yr, "MachOnly-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "MachOnly-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, name, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	defer purgeRootsByID(t, ctx, pool, []int64{yearID}, []int64{nid}, []int64{baseID})
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	mixerID, err := st.CreateMachine(ctx, baseID, "Zwangsmischer",
		decimal.RequireFromString("1.7"), decimal.RequireFromString("6.471"), "Sonstige", 1, decimal.Zero)
	if err != nil {
		t.Fatalf("machine: %v", err)
	}

	// 3 h at 11,00 €/h = 33,00 €, booked with NO tractor.
	e := &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Betonmischen",
		MachineLabels: "Zwangsmischer",
		Hours:         decimal.RequireFromString("3"),
		HourlyRate:    decimal.RequireFromString("11"),
		Cost:          decimal.RequireFromString("33"),
	}
	entryID, err := st.CreateEntry(ctx, e, []int64{mixerID})
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if got, err := st.GetEntry(ctx, entryID); err != nil {
		t.Fatalf("read back: %v", err)
	} else if got.TractorID != nil || got.LoadLevelID != nil {
		t.Fatalf("fixture is not machines-only: tractor=%v load=%v", got.TractorID, got.LoadLevelID)
	}

	// Unchanged basis: the preview must price it and report no change.
	rows, err := st.RecalcPreview(ctx, yearID, &nid)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("preview returned %d rows, want 1", len(rows))
	}
	if rows[0].NewRate.StringFixed(2) != "11.00" || rows[0].NewCost.StringFixed(2) != "33.00" {
		t.Errorf("unchanged basis: rate/cost = %s / %s, want 11.00 / 33.00",
			rows[0].NewRate.StringFixed(2), rows[0].NewCost.StringFixed(2))
	}
	if rows[0].Changed {
		t.Error("unchanged basis reported as changed")
	}

	// Raise the implement to 12,00 €/h. Without the fix this reported no change,
	// leaving the booking priced at the old rate for good.
	if err := st.UpdateMachine(ctx, mixerID, "Zwangsmischer",
		decimal.RequireFromString("1.7"), decimal.RequireFromString("7.0588"), "Sonstige", 1, decimal.Zero); err != nil {
		t.Fatalf("update machine: %v", err)
	}
	rows, err = st.RecalcPreview(ctx, yearID, &nid)
	if err != nil {
		t.Fatalf("preview after change: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("preview returned %d rows, want 1", len(rows))
	}
	r := rows[0]
	if !r.Changed {
		t.Error("price change not detected on a machines-only booking")
	}
	if r.OldRate.StringFixed(2) != "11.00" || r.OldCost.StringFixed(2) != "33.00" {
		t.Errorf("old = %s / %s, want 11.00 / 33.00", r.OldRate.StringFixed(2), r.OldCost.StringFixed(2))
	}
	if r.NewRate.StringFixed(2) != "12.00" || r.NewCost.StringFixed(2) != "36.00" {
		t.Errorf("new = %s / %s, want 12.00 / 36.00", r.NewRate.StringFixed(2), r.NewCost.StringFixed(2))
	}
	// No tractor was involved, so no tractor label may be invented for it.
	if r.TractorLabel != "" || r.LoadLabel != "" {
		t.Errorf("machines-only row carries tractor labels: %q / %q", r.TractorLabel, r.LoadLabel)
	}
	if r.MachineLabels != "Zwangsmischer" {
		t.Errorf("machine labels = %q, want %q", r.MachineLabels, "Zwangsmischer")
	}
}

// A quantity booking (Pauschale / ha / Ballen) also carries no tractor id, and its
// price is the unit price the user typed — nothing about it derives from the
// basis. Repricing one as if it were a machines-only rig yields rate 0 and cost 0,
// so applying the recalculation would wipe it out. The old tractor guard excluded
// these by accident; when the guard was relaxed for machines-only rigs this had to
// become deliberate, and this test is what pins it.
func TestRecalcLeavesQuantityBookingsAloneIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	yr := 4700 + os.Getpid()%1000
	name := fmt.Sprintf("Qty-Nachbar %d", os.Getpid())
	purgeFixtures(t, ctx, pool, fixtures{Years: []int{yr}, NeighborNames: []string{name}})

	baseID, err := st.CreateEmptyBase(ctx, yr, "Qty-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Qty-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, name, "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	defer purgeRootsByID(t, ctx, pool, []int64{yearID}, []int64{nid}, []int64{baseID})
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}
	// A machine exists in the basis but is attached to nothing — repricing the
	// quantity booking as a rig would still land on 0, which is the failure mode.
	if _, err := st.CreateMachine(ctx, baseID, "Zwangsmischer",
		decimal.RequireFromString("1.7"), decimal.RequireFromString("6.471"), "Sonstige", 1, decimal.Zero); err != nil {
		t.Fatalf("machine: %v", err)
	}

	for _, unit := range []string{"pauschal", "ha", "Ballen"} {
		if _, err := st.CreateEntry(ctx, &models.Entry{
			NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Pauschale " + unit,
			Unit:       unit,
			Quantity:   decimal.RequireFromString("2"),
			UnitPrice:  decimal.RequireFromString("25"),
			Hours:      decimal.Zero,
			HourlyRate: decimal.Zero,
			Cost:       decimal.RequireFromString("50"),
		}, nil); err != nil {
			t.Fatalf("entry %s: %v", unit, err)
		}
	}

	rows, err := st.RecalcPreview(ctx, yearID, &nid)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("preview returned %d rows, want 3", len(rows))
	}
	for _, r := range rows {
		if r.Changed {
			t.Errorf("%s: reported as changed — a quantity booking is not priced from the basis", r.TaskLabel)
		}
		if r.NewCost.StringFixed(2) != "50.00" {
			t.Errorf("%s: new cost = %s, want 50.00 (0.00 means it was repriced as a rig and would be wiped)",
				r.TaskLabel, r.NewCost.StringFixed(2))
		}
		if r.NewRate.StringFixed(2) != "0.00" {
			t.Errorf("%s: new rate = %s, want 0.00 unchanged", r.TaskLabel, r.NewRate.StringFixed(2))
		}
	}
}
