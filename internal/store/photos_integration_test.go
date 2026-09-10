package store_test

import (
	"bytes"
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

// TestEntryPhotosIntegration proves the photo round-trip: store bytes, read them
// back scoped to the booking, list/count, delete, and that a mismatched
// entry/photo pair is not served. Runs only when TEST_DATABASE_URL is set.
func TestEntryPhotosIntegration(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// As a Cleanup, not a defer: the purge below is also a Cleanup, and
	// Cleanups run LIFO after all defers — a deferred Close would slam the
	// pool shut before the purge gets to run.
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	pid := os.Getpid()
	// Purge BEFORE seeding, and check every error. Both were missing: the
	// fixture year and neighbor name are UNIQUE and derived from the pid, which
	// container runtimes reuse — so a second run with the same pid hit the
	// unique constraints, the discarded errors left baseID/yearID/nid at 0, and
	// the first insert failed as a foreign-key violation on id 0. That is what
	// made this suite intermittently red for reasons unrelated to the code.
	f := fixtures{Years: []int{4500 + pid%1000}, NeighborNames: []string{fmt.Sprintf("Foto Nachbar %d", pid)}}
	purgeFixtures(t, ctx, pool, f)
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f) })

	baseID, err := st.CreateEmptyBase(ctx, 4500+pid%1000, "Foto-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 4500+pid%1000, baseID, "Foto-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, fmt.Sprintf("Foto Nachbar %d", pid), "")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("membership: %v", err)
	}
	eid, err := st.CreateEntry(ctx, &models.Entry{
		NeighborID: nid, BillingYearID: yearID, Date: time.Now(), TaskLabel: "Foto",
		Unit: "h", Hours: decimal.RequireFromString("1"), HourlyRate: decimal.RequireFromString("40"),
		Cost: decimal.RequireFromString("40"),
	}, nil)
	if err != nil {
		t.Fatalf("entry: %v", err)
	}

	want := []byte{0xFF, 0xD8, 0xFF, 1, 2, 3, 0xFF, 0xD9} // JPEG-ish bytes
	pidPhoto, err := st.AddEntryPhoto(ctx, eid, want, "image/jpeg")
	if err != nil {
		t.Fatalf("add photo: %v", err)
	}

	got, ct, err := st.GetEntryPhoto(ctx, eid, pidPhoto)
	if err != nil {
		t.Fatalf("get photo: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("round-trip bytes differ: got %v", got)
	}
	if ct != "image/jpeg" {
		t.Errorf("content type = %q, want image/jpeg", ct)
	}

	// PhotoCounts replaced the per-entry counter: one query for a whole year,
	// keyed by booking.
	if counts, err := st.PhotoCounts(ctx, yearID, nid); err != nil || counts[eid] != 1 {
		t.Errorf("PhotoCounts[%d] = %d, %v; want 1, nil", eid, counts[eid], err)
	}
	if refs, err := st.ListNeighborPhotos(ctx, yearID, nid); err != nil || len(refs) != 1 || refs[0].EntryID != eid {
		t.Errorf("ListNeighborPhotos = %+v, %v; want one ref for entry %d", refs, err, eid)
	}
	if ps, _ := st.ListEntryPhotos(ctx, eid); len(ps) != 1 {
		t.Errorf("list len = %d, want 1", len(ps))
	}

	// A photo requested under the wrong entry must not be served.
	if _, _, err := st.GetEntryPhoto(ctx, eid+999, pidPhoto); err == nil {
		t.Errorf("cross-entry photo access should fail")
	}

	if err := st.DeleteEntryPhoto(ctx, eid, pidPhoto); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if counts, err := st.PhotoCounts(ctx, yearID, nid); err != nil || counts[eid] != 0 {
		t.Errorf("count after delete = %d, %v; want 0, nil", counts[eid], err)
	}
}
