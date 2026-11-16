//go:build integration

package server

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/store"
)

// TestCatalogWritesBoundToCheckedBaseIntegration pins the catalog lock: the
// handler checks the submitted base_id, so every write must also be bound to
// that base. An item of a locked base named together with an unlocked base_id
// stays untouched (Codex audit 2026-11, catalog/base-binding-lock-bypass).
func TestCatalogWritesBoundToCheckedBaseIntegration(t *testing.T) {
	e := newItEnv(t)
	ctx := e.ctx
	if _, err := e.pool.ExecContext(ctx, `DELETE FROM price_bases WHERE year=$1`, e.year+200_000); err != nil {
		t.Fatal(err)
	}
	locked, err := e.st.CreateEmptyBase(ctx, e.year+200_000, "Locked basis "+e.uname)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.pool.ExecContext(context.Background(), `DELETE FROM price_bases WHERE id=$1`, locked) })
	dec := decimal.RequireFromString
	loadID, err := e.st.CreateLoadLevel(ctx, locked, "locked-load", dec("0.30"), 1)
	if err != nil {
		t.Fatal(err)
	}
	tractorID, err := e.st.CreateTractor(ctx, locked, "LOCKED-T", "Locked tractor", dec("80"), 1)
	if err != nil {
		t.Fatal(err)
	}
	machineID, err := e.st.CreateMachine(ctx, locked, "Locked machine", dec("2"), dec("5"), "Lock", 1, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	gespannID, err := e.st.CreateGespann(ctx, locked, "Locked rig", &tractorID, &loadID, []int64{machineID}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetBaseLocked(ctx, locked, true); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var state string
		if err := e.pool.QueryRowContext(ctx, `SELECT concat_ws('|',
			(SELECT string_agg(concat_ws(',', id, name, cost_per_ps, sort_order), ';' ORDER BY id) FROM load_levels WHERE base_id=$1),
			(SELECT string_agg(concat_ws(',', id, ident, name, ps, active, sort_order), ';' ORDER BY id) FROM tractors WHERE base_id=$1),
			(SELECT string_agg(concat_ws(',', id, name, working_width, cost_per_ab, active, category, self_cost_per_h, acquisition_cost), ';' ORDER BY id) FROM machines WHERE base_id=$1),
			(SELECT string_agg(concat_ws(',', g.id, g.name, g.tractor_id, g.load_level_id, g.sort_order,
				(SELECT string_agg(machine_id::text, ',' ORDER BY machine_id) FROM gespann_machines WHERE gespann_id=g.id)), ';' ORDER BY g.id)
			   FROM gespanne g WHERE g.base_id=$1))`, locked).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	before := snapshot()
	unlocked := itoa64(e.baseID64)
	for _, step := range []struct {
		path string
		form url.Values
	}{
		{"/prices/loadlevels", url.Values{"id": {itoa64(loadID)}, "name": {"changed"}, "cost_per_ps": {"9"}, "sort_order": {"9"}}},
		{"/prices/loadlevels/" + itoa64(loadID) + "/delete", url.Values{}},
		{"/prices/tractors", url.Values{"id": {itoa64(tractorID)}, "ident": {"CHANGED"}, "name": {"changed"}, "ps": {"999"}, "sort_order": {"9"}}},
		{"/prices/tractors/" + itoa64(tractorID) + "/toggle", url.Values{"active": {"false"}}},
		{"/prices/tractors/" + itoa64(tractorID) + "/delete", url.Values{}},
		{"/prices/machines", url.Values{"id": {itoa64(machineID)}, "name": {"changed"}, "working_width": {"3"},
			"cost_per_ab": {"7"}, "category": {"Changed"}, "sort_order": {"2"}, "self_cost_per_h": {"1"}}},
		{"/prices/machines/" + itoa64(machineID) + "/cost-model", url.Values{"acquisition_cost": {"1000"}}},
		{"/prices/machines/" + itoa64(machineID) + "/toggle", url.Values{"active": {"false"}}},
		{"/prices/machines/" + itoa64(machineID) + "/delete", url.Values{}},
		{"/gespanne", url.Values{"id": {itoa64(gespannID)}, "name": {"changed"}, "machine_ids": {itoa64(e.machineID)}}},
		{"/gespanne/" + itoa64(gespannID) + "/delete", url.Values{}},
	} {
		step.form.Set("base_id", unlocked)
		e.post(step.path, step.form)
		if after := snapshot(); after != before {
			t.Fatalf("POST %s with the unlocked base_id changed the locked basis:\nbefore %s\nafter  %s", step.path, before, after)
		}
	}

	// The store refuses on its own, independent of the handler's check.
	if err := e.st.UpdateMachine(ctx, locked, machineID, "changed", dec("3"), dec("7"), "", 1, decimal.Zero); !errors.Is(err, store.ErrBaseLocked) {
		t.Fatalf("update in a locked base = %v, want ErrBaseLocked", err)
	}
	if _, err := e.st.CreateMachine(ctx, locked, "new", dec("1"), dec("1"), "", 1, decimal.Zero); !errors.Is(err, store.ErrBaseLocked) {
		t.Fatalf("create in a locked base = %v, want ErrBaseLocked", err)
	}
	if err := e.st.DeleteTractor(ctx, e.baseID64, tractorID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete through another base = %v, want ErrNotFound", err)
	}
	// A rig is priced from its own base only.
	if _, err := e.st.CreateGespann(ctx, e.baseID64, "Foreign parts "+e.uname, &tractorID, &e.loadID, nil, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rig with a foreign tractor = %v, want ErrNotFound", err)
	}
	if err := e.st.UpdateGespann(ctx, e.baseID64, e.gespannID, "IT-Gespann", &e.tractorID, &e.loadID, []int64{e.machineID, machineID}, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rig with a foreign machine = %v, want ErrNotFound", err)
	}
	if after := snapshot(); after != before {
		t.Fatalf("store calls changed the locked basis:\nbefore %s\nafter  %s", before, after)
	}
	// The unlocked base still accepts its own items.
	if err := e.st.UpdateGespann(ctx, e.baseID64, e.gespannID, "IT-Gespann", &e.tractorID, &e.loadID, []int64{e.machineID}, 1); err != nil {
		t.Fatalf("rig update within its base: %v", err)
	}
}
