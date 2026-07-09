package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/db"
	"treckrr/internal/models"
	"treckrr/internal/store"
)

// TestRecalcIntegration proves that editing a basis makes bookings "stale" and
// that recalculation re-prices them from the current basis values, leaving
// voided bookings untouched. Runs only when TEST_DATABASE_URL is set.
func TestRecalcIntegration(t *testing.T) {
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

	baseID, err := st.CreateEmptyBase(ctx, 2096, "Recalc-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2096, baseID, "Recalc-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, "Recalc-Nachbar", "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	defer func() {
		_, _ = pool.ExecContext(ctx, `DELETE FROM billing_years WHERE id=$1`, yearID)
		_, _ = pool.ExecContext(ctx, `DELETE FROM price_bases WHERE id=$1`, baseID)
		_, _ = pool.ExecContext(ctx, `DELETE FROM neighbors WHERE id=$1`, nid)
	}()
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add neighbor: %v", err)
	}

	loadID, err := st.CreateLoadLevel(ctx, baseID, "mittel", decimal.RequireFromString("0.36"), 1)
	if err != nil {
		t.Fatalf("load level: %v", err)
	}
	tractorID, err := st.CreateTractor(ctx, baseID, "T1", "", decimal.RequireFromString("100"), 1)
	if err != nil {
		t.Fatalf("tractor: %v", err)
	}

	// Book an entry at the current rate: 100 PS × 0.36 = 36.00/h, 2 h → 72.00.
	e := &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Mähen",
		TractorID: &tractorID, LoadLevelID: &loadID, TractorLabel: "T1 (100 PS)", LoadLabel: "mittel",
		Hours: decimal.RequireFromString("2"), HourlyRate: decimal.RequireFromString("36"), Cost: decimal.RequireFromString("72"),
	}
	entryID, err := st.CreateEntry(ctx, e, nil)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}
	// A voided booking that must never be touched by recalc.
	ev := &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Storniert",
		TractorID: &tractorID, LoadLevelID: &loadID, TractorLabel: "T1 (100 PS)", LoadLabel: "mittel",
		Hours: decimal.RequireFromString("1"), HourlyRate: decimal.RequireFromString("36"), Cost: decimal.RequireFromString("36"),
	}
	voidID, err := st.CreateEntry(ctx, ev, nil)
	if err != nil {
		t.Fatalf("void entry: %v", err)
	}
	if err := st.SetEntryVoided(ctx, voidID, true, "test"); err != nil {
		t.Fatalf("void: %v", err)
	}

	// Nothing stale yet.
	rows, err := st.RecalcPreview(ctx, yearID, nil)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, r := range rows {
		if r.Changed {
			t.Fatalf("unexpected stale row before basis edit: %+v", r)
		}
	}

	// Edit the basis: load level 0.36 → 0.40 → new rate 40.00/h, 2 h → 80.00.
	if err := st.UpdateLoadLevel(ctx, loadID, "mittel", decimal.RequireFromString("0.40"), 1); err != nil {
		t.Fatalf("update load: %v", err)
	}

	rows, err = st.RecalcPreview(ctx, yearID, nil)
	if err != nil {
		t.Fatalf("preview 2: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("preview should have 1 non-voided row, got %d", len(rows))
	}
	r0 := rows[0]
	if !r0.Changed || !r0.NewRate.Equal(decimal.RequireFromString("40")) || !r0.NewCost.Equal(decimal.RequireFromString("80")) {
		t.Fatalf("preview row = changed:%v rate:%s cost:%s, want changed:true 40 80", r0.Changed, r0.NewRate, r0.NewCost)
	}

	// Apply and verify the entry was re-priced; totals reported for audit.
	updated, oldT, newT, err := st.ApplyRecalc(ctx, yearID, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if updated != 1 || !oldT.Equal(decimal.RequireFromString("72")) || !newT.Equal(decimal.RequireFromString("80")) {
		t.Fatalf("apply = updated:%d old:%s new:%s, want 1 72 80", updated, oldT, newT)
	}
	got, err := st.GetEntry(ctx, entryID)
	if err != nil {
		t.Fatalf("get entry: %v", err)
	}
	if !got.HourlyRate.Equal(decimal.RequireFromString("40")) || !got.Cost.Equal(decimal.RequireFromString("80")) {
		t.Fatalf("recalced entry = %s/%s, want 40/80", got.HourlyRate, got.Cost)
	}
	// The voided entry must be unchanged.
	gotVoid, err := st.GetEntry(ctx, voidID)
	if err != nil {
		t.Fatalf("get void entry: %v", err)
	}
	if !gotVoid.Cost.Equal(decimal.RequireFromString("36")) {
		t.Fatalf("voided entry changed to %s, want 36 (untouched)", gotVoid.Cost)
	}
}
