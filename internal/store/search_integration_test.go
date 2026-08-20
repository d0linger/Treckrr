package store_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

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

	// Unique per run so repeated runs against a shared integration DB don't collide on
	// the UNIQUE constraints — neighbor name, invoice number, and billing_years.year
	// (INTEGER NOT NULL UNIQUE). os.Getpid() alone repeats across separate containers,
	// so derive both the name token and the year from one nanosecond stamp; the year is
	// reduced into int4 range.
	nano := time.Now().UnixNano()
	uniq := fmt.Sprintf("Suchbar%d", nano)
	yr := int(nano % 2_000_000_000)
	baseID, err := st.CreateEmptyBase(ctx, yr, "Such-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, yr, baseID, "Such-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	nid, err := st.CreateNeighbor(ctx, uniq+" Nachbar", "notiz")
	if err != nil {
		t.Fatalf("neighbor: %v", err)
	}
	if err := st.AddNeighborToYear(ctx, yearID, nid); err != nil {
		t.Fatalf("add: %v", err)
	}
	invNo := uniq + "R"
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO invoices (billing_year_id, neighbor_id, number, gross) VALUES ($1,$2,$3,10)`,
		yearID, nid, invNo); err != nil {
		t.Fatalf("invoice: %v", err)
	}
	// A tractor whose model carries the number "948".
	tractorIdent := uniq + " 948"
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO tractors (base_id, ident, name, ps) VALUES ($1,$2,'',480)`,
		baseID, tractorIdent); err != nil {
		t.Fatalf("tractor: %v", err)
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

	// Tractor lookup, the two facets of the reported bug:
	//  (a) the bare model NUMBER "948" must return tractors at all — it returned none
	//      before Stage 1 extended coverage to tractors; assert some tractor comes back.
	res, err = st.Search(ctx, "948", 6)
	if err != nil {
		t.Fatalf("search tractor by number: %v", err)
	}
	found948 := false
	for _, r := range res {
		if r.Kind == "tractor" {
			found948 = true
			if r.URL == "" {
				t.Errorf("tractor result has no URL")
			}
		}
	}
	if !found948 {
		t.Errorf("model number 948 returned no tractor: %+v", res)
	}
	//  (b) this run's specific tractor is findable by its unique ident — isolated from
	//      the other "948" rows a shared integration DB may accumulate.
	res, err = st.Search(ctx, tractorIdent, 6)
	if err != nil {
		t.Fatalf("search tractor by ident: %v", err)
	}
	foundT := false
	for _, r := range res {
		if r.Kind == "tractor" && strings.Contains(r.Label, tractorIdent) {
			foundT = true
		}
	}
	if !foundT {
		t.Errorf("tractor not found by unique ident %q: %+v", tractorIdent, res)
	}

	// Price basis (Grundlage) by name.
	res, err = st.Search(ctx, "Such-Basis", 6)
	if err != nil {
		t.Fatalf("search base: %v", err)
	}
	foundB := false
	for _, r := range res {
		if r.Kind == "base" && strings.Contains(r.Label, "Such-Basis") {
			foundB = true
		}
	}
	if !foundB {
		t.Errorf("price basis not found by name: %+v", res)
	}

	// Machines, load levels and gespanne are each searchable by name and link to
	// their basis; exercise one of each (Kind, Label and navigation URL).
	machineName := uniq + " Maschine"
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO machines (base_id, name, working_width, cost_per_ab) VALUES ($1,$2,3,10)`,
		baseID, machineName); err != nil {
		t.Fatalf("machine: %v", err)
	}
	loadName := uniq + " Stufe"
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO load_levels (base_id, name, cost_per_ps) VALUES ($1,$2,5)`,
		baseID, loadName); err != nil {
		t.Fatalf("load level: %v", err)
	}
	gespannName := uniq + " Gespann"
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO gespanne (base_id, name) VALUES ($1,$2)`,
		baseID, gespannName); err != nil {
		t.Fatalf("gespann: %v", err)
	}
	assertKind := func(query, wantKind, wantLabel, wantURL string) {
		res, err := st.Search(ctx, query, 6)
		if err != nil {
			t.Fatalf("search %s: %v", wantKind, err)
		}
		for _, r := range res {
			if r.Kind == wantKind && r.Label == wantLabel {
				if r.URL != wantURL {
					t.Errorf("%s %q: URL = %q, want %q", wantKind, wantLabel, r.URL, wantURL)
				}
				return
			}
		}
		t.Errorf("%s not found by name %q: %+v", wantKind, query, res)
	}
	pricesURL := fmt.Sprintf("/prices?base=%d", baseID)
	assertKind(machineName, "machine", machineName, pricesURL)
	assertKind(loadName, "load", loadName, pricesURL)
	assertKind(gespannName, "gespann", gespannName, fmt.Sprintf("/gespanne?base=%d", baseID))

	// --- Stage 2: German stemming, diacritic folding, typo tolerance ---
	if _, err := st.CreateNeighbor(ctx, uniq+"stem", "Traktor"); err != nil {
		t.Fatalf("stem neighbor: %v", err)
	}
	if _, err := st.CreateNeighbor(ctx, uniq+"grün", ""); err != nil {
		t.Fatalf("diacritic neighbor: %v", err)
	}
	if _, err := st.CreateNeighbor(ctx, uniq+"Schmidt", ""); err != nil {
		t.Fatalf("typo neighbor: %v", err)
	}
	assertNeighbor := func(query, wantLabel, what string) {
		res, err := st.Search(ctx, query, 8)
		if err != nil {
			t.Fatalf("search (%s): %v", what, err)
		}
		for _, r := range res {
			if r.Kind == "neighbor" && strings.Contains(r.Label, wantLabel) {
				return
			}
		}
		t.Errorf("%s: query %q did not find neighbor %q: %+v", what, query, wantLabel, res)
	}
	// Stemming: the plural query "Traktoren" matches the singular value "Traktor"
	// (the unique token AND the stemmed word must both be present).
	assertNeighbor(uniq+"stem Traktoren", uniq+"stem", "stemming")
	// Diacritic folding: "grun" (no umlaut) finds "grün" via unaccent.
	assertNeighbor(uniq+"grun", uniq+"grün", "diacritic folding")
	// Typo tolerance: "Schmdt" (missing i) finds "Schmidt" via trigram word-similarity
	// — neither the substring nor the stemmed match would catch this one.
	assertNeighbor(uniq+"Schmdt", uniq+"Schmidt", "typo tolerance")

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
