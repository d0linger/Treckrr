package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestPricingFreshnessGateIntegration pins the 0040 staleness gate, which lets
// the dashboard and neighbor-detail pages skip the full repricing simulation.
//
// The gate is only safe because it is exact-NEGATIVE: zero means nothing can be
// stale, so the caller skips the expensive path. A wrongly-zero answer would
// silently hide bookings that need recalculating — which is why the bump is a DB
// trigger rather than application code, and why the dangerous direction ("the
// trigger did not fire") is what this test drives.
func TestPricingFreshnessGateIntegration(t *testing.T) {
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

	f := fixtures{Years: []int{2090}, NeighborNames: []string{"Frische-Nachbar"}}
	purgeFixtures(t, ctx, pool, f)
	defer purgeFixtures(t, ctx, pool, f)

	baseID, err := st.CreateEmptyBase(ctx, 2090, "Frische-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	loadID, err := st.CreateLoadLevel(ctx, baseID, "mittel", decimal.RequireFromString("0.36"), 1)
	if err != nil {
		t.Fatalf("load level: %v", err)
	}
	tractorID, err := st.CreateTractor(ctx, baseID, "T1", "", decimal.RequireFromString("100"), 1)
	if err != nil {
		t.Fatalf("tractor: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2090, baseID, "Frische-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Frische-Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add to year: %v", err)
	}

	// Priced at the CURRENT basis: 100 PS × 0.36 = 36.00/h, 2 h -> 72.00.
	if _, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Mähen",
		TractorID: &tractorID, LoadLevelID: &loadID,
		TractorLabel: "T1 (100 PS)", LoadLabel: "mittel",
		Hours:      decimal.RequireFromString("2"),
		HourlyRate: decimal.RequireFromString("36"),
		Cost:       decimal.RequireFromString("72"),
	}, nil); err != nil {
		t.Fatalf("entry: %v", err)
	}

	gate := func(who string) int {
		t.Helper()
		n, err := st.CountPotentiallyStale(ctx, yearID, nil)
		if err != nil {
			t.Fatalf("gate (%s): %v", who, err)
		}
		return n
	}

	if got := gate("fresh"); got != 0 {
		t.Fatalf("gate = %d straight after booking, want 0 — the expensive preview "+
			"would run on every dashboard render for nothing", got)
	}

	// Edit the basis. The trigger must bump price_bases.items_updated_at; if a
	// write path could bypass it, the gate would report 0 and hide the repricing.
	if err := st.UpdateLoadLevel(ctx, loadID, "mittel", decimal.RequireFromString("0.40"), 1); err != nil {
		t.Fatalf("update load: %v", err)
	}
	if got := gate("after basis edit"); got != 1 {
		t.Fatalf("gate = %d after editing the basis, want 1 — a wrongly-zero gate "+
			"silently hides bookings that need recalculating", got)
	}
	if n, err := st.CountPotentiallyStale(ctx, yearID, &nid); err != nil || n != 1 {
		t.Fatalf("per-neighbor gate = %d (err %v), want 1", n, err)
	}

	// The exact simulation must agree, so the gate is not firing on noise.
	rows, err := st.RecalcPreview(ctx, yearID, nil)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	changed := 0
	for _, r := range rows {
		if r.Changed {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("RecalcPreview reports %d changed, want 1", changed)
	}

	// Applying the recalc restamps priced_at, closing the gate again — otherwise
	// the badge would stay lit forever and the preview run on every render.
	if _, _, _, err := st.ApplyRecalc(ctx, yearID, nil); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := gate("after recalc"); got != 0 {
		t.Fatalf("gate = %d after applying the recalc, want 0 — priced_at was not "+
			"restamped", got)
	}
}
