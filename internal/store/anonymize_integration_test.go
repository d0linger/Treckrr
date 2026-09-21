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
	// Cleanup statt defer: die Purge unten ist ebenfalls ein Cleanup, und
	// Cleanups laufen LIFO NACH allen defers — ein deferred Close macht den
	// Pool zu, bevor die Purge dran ist.
	t.Cleanup(func() { _ = pool.Close() })
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	st := store.New(pool, "test-encryption-secret")

	// The year fixture is UNIQUE in the shared DB — purge before AND after so a
	// rerun (or a crashed run) cannot collide.
	f := fixtures{Years: []int{2106}}
	purgeFixtures(t, ctx, pool, f)
	t.Cleanup(func() { purgeFixtures(t, ctx, pool, f) })

	name := fmt.Sprintf("Anon Test %d", os.Getpid()) // unique for the shared DB
	id, err := st.CreateNeighbor(ctx, name, "eine Notiz")
	if err != nil {
		t.Fatalf("create neighbor: %v", err)
	}
	if err := st.UpdateNeighbor(ctx, id, name, "eine Notiz", "Dorfstraße 1", "ATU99999999", "kunde@example.at", "AT611904300234573201", nil); err != nil {
		t.Fatalf("update neighbor: %v", err)
	}
	if _, err := pool.ExecContext(ctx, `INSERT INTO neighbor_equipment
		(neighbor_id, name, capacity, capacity_unit, billing_unit, default_rate, note)
		VALUES ($1, 'Historischer Mischer', 1000, 'l', 'h', 12, 'Altbestand')`, id); err != nil {
		t.Fatalf("seed legacy neighbor equipment: %v", err)
	}
	legacyEquipment, err := st.ListNeighborEquipment(ctx, id)
	if err != nil || len(legacyEquipment) != 1 || legacyEquipment[0].Name != "Historischer Mischer" {
		t.Fatalf("list legacy neighbor equipment: %+v, %v", legacyEquipment, err)
	}

	// Seed the free-text surfaces the scrub list once missed: a Ratenplan note
	// and a recurring rule (whose frozen template names the person AND would
	// keep booking for them after erasure). Self-sufficient: own base+year, so
	// the test does not depend on what other suites left in the shared DB.
	baseID, err := st.CreateEmptyBase(ctx, 2106, "Anon-Basis")
	if err != nil {
		t.Fatalf("base: %v", err)
	}
	yearID, err := st.CreateBillingYear(ctx, 2106, baseID, "Anon-Jahr")
	if err != nil {
		t.Fatalf("year: %v", err)
	}
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO payment_plans (billing_year_id, neighbor_id, due_on, amount, note)
		VALUES ($1, $2, now(), 10, 'zahlt monatlich, Sohn Josef holt das Geld')`, yearID, id); err != nil {
		t.Fatalf("seed payment plan: %v", err)
	}
	if _, err := pool.ExecContext(ctx, `
		INSERT INTO recurring_entries (neighbor_id, template, interval_kind, next_run, active)
		VALUES ($1, '{"task_label":"Melken bei Anon"}'::jsonb, 'monthly', now(), TRUE)`, id); err != nil {
		t.Fatalf("seed recurring rule: %v", err)
	}

	if err := st.AnonymizeNeighbor(ctx, id); err != nil {
		t.Fatalf("anonymize: %v", err)
	}
	var planNotes, rules, legacyEquipmentRows int
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM payment_plans WHERE neighbor_id=$1 AND note <> ''`, id).Scan(&planNotes); err != nil {
		t.Fatalf("count plan notes: %v", err)
	}
	if planNotes != 0 {
		t.Errorf("payment_plans.note survived anonymization (%d rows)", planNotes)
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM recurring_entries WHERE neighbor_id=$1`, id).Scan(&rules); err != nil {
		t.Fatalf("count recurring: %v", err)
	}
	if rules != 0 {
		t.Errorf("recurring rules survived anonymization (%d rows) — they would keep booking for an erased person", rules)
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT count(*) FROM neighbor_equipment WHERE neighbor_id=$1`, id).Scan(&legacyEquipmentRows); err != nil {
		t.Fatalf("count legacy neighbor equipment: %v", err)
	}
	if legacyEquipmentRows != 0 {
		t.Errorf("legacy neighbor equipment survived anonymization (%d rows)", legacyEquipmentRows)
	}
	n, err := st.GetNeighbor(ctx, id)
	if err != nil {
		t.Fatalf("get after anonymize: %v", err)
	}
	if !n.Anonymized || !n.Archived {
		t.Errorf("expected anonymized+archived, got anonymized=%v archived=%v", n.Anonymized, n.Archived)
	}
	if n.Note != "" || n.Address != "" || n.TaxID != "" || n.Email != "" || n.IBAN != "" {
		t.Errorf("PII not cleared: note=%q address=%q tax_id=%q email=%q iban=%q", n.Note, n.Address, n.TaxID, n.Email, n.IBAN)
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
