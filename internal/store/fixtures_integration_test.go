//go:build integration

package store_test

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/db"
)

// TestScratchStoreIgnoresDatabaseQueryOverride checks that URL dbname query
// overrides cannot redirect the store fixture away from its scratch database,
// including when the override names the parent or a nonexistent database.
func TestScratchStoreIgnoresDatabaseQueryOverride(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	base, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := db.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	var parentName string
	if err := parent.QueryRowContext(t.Context(), `SELECT current_database()`).Scan(&parentName); err != nil {
		t.Fatal(err)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	missingName := "treckrr_missing_" + hex.EncodeToString(nonce[:])
	var exists bool
	if err := parent.QueryRowContext(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)`, missingName).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("missing-database fixture unexpectedly exists")
	}
	tests := []struct {
		name     string
		database string
	}{
		{name: "parent database override", database: parentName},
		// This also fails if the admin connection retains the override.
		{name: "nonexistent database override", database: missingName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configured := *base
			query := configured.Query()
			query.Set("dbname", tt.database)
			configured.RawQuery = query.Encode()
			t.Setenv("TEST_DATABASE_URL", configured.String())

			_, pool := scratchStore(t)
			var current string
			if err := pool.QueryRowContext(t.Context(), `SELECT current_database()`).Scan(&current); err != nil {
				t.Fatal(err)
			}
			if current == parentName || !strings.HasPrefix(current, "treckrr_test_") {
				t.Fatalf("scratch database = %q, want an isolated database distinct from %q", current, parentName)
			}
		})
	}
}
