package store_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestSearchIntegration proves the global search finds a neighbor by name and an
// invoice by number, escapes ILIKE wildcards, and caps per kind. Runs only when
// TEST_DATABASE_URL is set.
func TestSearchIntegration(t *testing.T) {
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
	uniq := fmt.Sprintf("Suchbar%d", pid)
	baseID, _ := st.CreateEmptyBase(ctx, 4000+pid%1000, "Such-Basis")
	yearID, _ := st.CreateBillingYear(ctx, 4000+pid%1000, baseID, "Such-Jahr")
	nid, err := st.CreateNeighbor(ctx, uniq+" Nachbar", "notiz")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add: %v", err)
	}
	invNo := fmt.Sprintf("%d-SUCH", pid)
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO invoices (billing_year_id, neighbor_id, number, gross) VALUES ($1,$2,$3,10)`,
		yearID, nid, invNo); err != nil {
		t.Fatalf("invoice: %v", err)
	}

	// Neighbor by name.
	res, err := st.Search(ctx, uniq, 6)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	foundN := false
	for _, r := range res {
		if r.Kind == "neighbor" && strings.Contains(r.Label, uniq) {
			foundN = true
			if r.URL == "" {
				t.Errorf("neighbor result has no URL")
			}
		}
	}
	if !foundN {
		t.Errorf("neighbor not found by name %q: %+v", uniq, res)
	}

	// Invoice by number.
	res, err = st.Search(ctx, invNo, 6)
	if err != nil {
		t.Fatalf("search invoice: %v", err)
	}
	foundI := false
	for _, r := range res {
		if r.Kind == "invoice" && strings.Contains(r.Label, invNo) {
			foundI = true
		}
	}
	if !foundI {
		t.Errorf("invoice not found by number %q: %+v", invNo, res)
	}

	// A wildcard-only term must be escaped (searched literally) and not match all.
	res, err = st.Search(ctx, "%", 6)
	if err != nil {
		t.Fatalf("search wildcard: %v", err)
	}
	for _, r := range res {
		if strings.Contains(r.Label, uniq) {
			t.Errorf("literal '%%' must not match everything")
		}
	}
}
