//go:build integration

package store_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/d0linger/treckrr/internal/store"
)

type rigQueryCounter struct{ queries atomic.Int64 }

func (c *rigQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "FROM gespanne") || strings.Contains(data.SQL, "FROM gespann_machines") {
		c.queries.Add(1)
	}
	return ctx
}

func (*rigQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestListGespanneBatchIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := context.Background()
	// Connect the query tracer to this test's owned scratch database, never the
	// shared test database. Count real driver calls rather than SQL mock shapes.
	config, err := pgx.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRowContext(ctx, `SELECT current_database()`).Scan(&config.Database); err != nil {
		t.Fatal(err)
	}
	counter := &rigQueryCounter{}
	config.Tracer = counter
	tracedDB := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = tracedDB.Close() })
	traced := store.New(tracedDB, "test-encryption-secret")
	baseID, err := st.CreateEmptyBase(ctx, 2026, "Rig test")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := traced.ListGespanne(ctx, baseID); err != nil || len(got) != 0 {
		t.Fatalf("empty base: %v, %v", got, err)
	}
	if got := counter.queries.Swap(0); got != 1 {
		t.Fatalf("empty list used %d queries, want 1", got)
	}
	machineIDs := make([]int64, 0, 3)
	for _, name := range []string{"M1", "M2", "M3"} {
		id, err := st.CreateMachine(ctx, baseID, name, dec("1"), dec("2"), "Test", 1, dec("0"))
		if err != nil {
			t.Fatal(err)
		}
		machineIDs = append(machineIDs, id)
	}
	emptyID, err := st.CreateGespann(ctx, baseID, "Empty", nil, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"A", "B", "C"} {
		if _, err := st.CreateGespann(ctx, baseID, name, nil, nil,
			[]int64{machineIDs[2], machineIDs[0]}, 1); err != nil {
			t.Fatal(err)
		}
	}
	rigs, err := traced.ListGespanne(ctx, baseID)
	if err != nil || len(rigs) != 4 {
		t.Fatalf("rigs = %v (%v)", rigs, err)
	}
	if got := counter.queries.Load(); got != 2 {
		t.Fatalf("four rigs used %d queries, want 2", got)
	}
	for i, rig := range rigs {
		if i == 0 {
			if rig.ID != emptyID || len(rig.MachineIDs) != 0 {
				t.Fatalf("empty rig changed: %+v", rig)
			}
			continue
		}
		if !reflect.DeepEqual(rig.MachineIDs, []int64{machineIDs[0], machineIDs[2]}) {
			t.Fatalf("rig %s machine ids = %v", rig.Name, rig.MachineIDs)
		}
	}
	if err := st.DeleteMachine(ctx, machineIDs[2]); err != nil {
		t.Fatal(err)
	}
	counter.queries.Store(0)
	rigs, err = traced.ListGespanne(ctx, baseID)
	if err != nil {
		t.Fatal(err)
	}
	if got := counter.queries.Load(); got != 2 {
		t.Fatalf("list after machine deletion used %d queries, want 2", got)
	}
	for _, rig := range rigs {
		if rig.ID != emptyID && !reflect.DeepEqual(rig.MachineIDs, []int64{machineIDs[0]}) {
			t.Fatalf("rig %s retained a deleted machine: %v", rig.Name, rig.MachineIDs)
		}
	}
}
