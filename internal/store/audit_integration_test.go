package store_test

import (
	"context"
	"os"
	"testing"

	"treckrr/internal/db"
	"treckrr/internal/store"
)

// TestAuditFilterPaginationIntegration verifies the SQL-backed audit filtering,
// counting and paging (which must cover the full history, not a fixed recent
// batch). Runs only when TEST_DATABASE_URL is set.
func TestAuditFilterPaginationIntegration(t *testing.T) {
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

	// Insert 120 rows with test-unique actions so assertions are independent of
	// any other rows in the table: 80x itest_a, 40x itest_b, one carrying a
	// unique search marker.
	const marker = "zzsearchmarker"
	for i := 0; i < 120; i++ {
		action := "itest_a"
		if i%3 == 0 {
			action = "itest_b"
		}
		detail := ""
		if i == 5 { // i%3==2 -> itest_a, carries the marker
			detail = marker
		}
		if err := st.AddAudit(ctx, nil, "tester", action, "auth", "", detail, "10.0.0.1"); err != nil {
			t.Fatalf("add audit %d: %v", i, err)
		}
	}

	// Search matches exactly the one marker row.
	if n, err := st.CountAudit(ctx, marker, ""); err != nil || n != 1 {
		t.Fatalf("CountAudit(marker) = %d, %v; want 1, nil", n, err)
	}
	// Action filter counts the full history, not a recent slice.
	if n, err := st.CountAudit(ctx, "", "itest_b"); err != nil || n != 40 {
		t.Fatalf("CountAudit(itest_b) = %d, %v; want 40", n, err)
	}
	if n, err := st.CountAudit(ctx, "", "itest_a"); err != nil || n != 80 {
		t.Fatalf("CountAudit(itest_a) = %d, %v; want 80", n, err)
	}

	// Paging: first page full, later page holds the remainder.
	p1, err := st.ListAuditFiltered(ctx, "", "itest_a", 50, 0)
	if err != nil || len(p1) != 50 {
		t.Fatalf("page1 len = %d, %v; want 50", len(p1), err)
	}
	p2, err := st.ListAuditFiltered(ctx, "", "itest_a", 50, 50)
	if err != nil || len(p2) != 30 {
		t.Fatalf("page2 len = %d, %v; want 30 (80 total - 50)", len(p2), err)
	}

	// Distinct actions include the ones we inserted.
	acts, err := st.AuditActions(ctx)
	if err != nil {
		t.Fatalf("AuditActions: %v", err)
	}
	seen := map[string]bool{}
	for _, a := range acts {
		seen[a] = true
	}
	if !seen["itest_a"] || !seen["itest_b"] {
		t.Fatalf("AuditActions missing inserted actions: %v", acts)
	}
}
