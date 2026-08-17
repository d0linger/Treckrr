package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
	"github.com/d0linger/treckrr/internal/store"
)

// TestAnonymizeNeighborIntegration proves DSGVO Art. 17 anonymization: the live
// personal data is cleared and the row flagged/archived, a second call is a
// no-op, and a missing id returns ErrNotFound. Runs only when TEST_DATABASE_URL
// is set.
func TestAnonymizeNeighborIntegration(t *testing.T) {
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

	name := fmt.Sprintf("Anon Test %d", os.Getpid()) // unique for the shared DB
	id, err := st.CreateNeighbor(ctx, name, "eine Notiz")
	if err != nil {
		t.Fatalf("create neighbor: %v", err)
	}
	if err := st.UpdateNeighbor(ctx, id, name, "eine Notiz", "Dorfstraße 1", "ATU99999999", "kunde@example.at"); err != nil {
		t.Fatalf("update neighbor: %v", err)
	}

	if err := st.AnonymizeNeighbor(ctx, id); err != nil {
		t.Fatalf("anonymize: %v", err)
	}
	n, err := st.GetNeighbor(ctx, id)
	if err != nil {
		t.Fatalf("get after anonymize: %v", err)
	}
	if !n.Anonymized || !n.Archived {
		t.Errorf("expected anonymized+archived, got anonymized=%v archived=%v", n.Anonymized, n.Archived)
	}
	if n.Note != "" || n.Address != "" || n.TaxID != "" || n.Email != "" {
		t.Errorf("PII not cleared: note=%q address=%q tax_id=%q email=%q", n.Note, n.Address, n.TaxID, n.Email)
	}
	if strings.Contains(n.Name, "Anon Test") {
		t.Errorf("name still identifying: %q", n.Name)
	}
	if !strings.HasPrefix(n.Name, "anonymisiert #") {
		t.Errorf("name should be the placeholder, got %q", n.Name)
	}

	// Second call is a no-op (still succeeds, nothing changes).
	if err := st.AnonymizeNeighbor(ctx, id); err != nil {
		t.Fatalf("second anonymize should be a no-op, got: %v", err)
	}

	// Missing id → ErrNotFound.
	if err := st.AnonymizeNeighbor(ctx, 999999999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing id, got %v", err)
	}
}
