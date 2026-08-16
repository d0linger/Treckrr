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
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	pid := os.Getpid()
	baseID, _ := st.CreateEmptyBase(ctx, 4500+pid%1000, "Foto-Basis")
	yearID, _ := st.CreateBillingYear(ctx, 4500+pid%1000, baseID, "Foto-Jahr")
	nid, _ := st.CreateNeighbor(ctx, fmt.Sprintf("Foto Nachbar %d", pid), "")
	_ = st.AddNeighborToYear(ctx, yearID, nid)
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

	if n, _ := st.CountEntryPhotos(ctx, eid); n != 1 {
		t.Errorf("count = %d, want 1", n)
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
	if n, _ := st.CountEntryPhotos(ctx, eid); n != 0 {
		t.Errorf("count after delete = %d, want 0", n)
	}
}
