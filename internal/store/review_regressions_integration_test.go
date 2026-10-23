//go:build integration

package store_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

func TestConcurrentRemovalKeepsRequiredFactorIntegration(t *testing.T) {
	st, _ := scratchStore(t)
	ctx := t.Context()
	id, err := st.CreateUser(ctx, "required-factor", "synthetic-password-123", models.RoleEditor)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: id, Enabled: true, Secret: totpTestFixture, RecoveryHashes: []string{"synthetic-recovery"}}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := st.AddWebauthnCredential(ctx, id, models.WebauthnCredential{CredentialID: []byte{byte(i + 1)}, PublicKey: []byte("synthetic-key")}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpdateSecurityPolicy(ctx, false, true, 0); err != nil {
		t.Fatal(err)
	}
	creds, err := st.ListWebauthnCredentials(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 3)
	for _, credential := range creds {
		go func() { <-start; _, err := st.DeleteWebauthnCredential(ctx, id, credential.ID); results <- err }()
	}
	go func() { <-start; results <- st.ConfigureTwoFactor(ctx, store.TwoFactorChange{UserID: id}) }()
	close(start)
	removed, refused := 0, 0
	for range 3 {
		switch err := <-results; {
		case err == nil:
			removed++
		case errors.Is(err, store.ErrSecondFactorRequired):
			refused++
		default:
			t.Fatalf("remove factor: %v", err)
		}
	}
	u, err := st.GetUser(ctx, id)
	if err != nil || !u.HasSecondFactor() || removed != 2 || refused != 1 {
		t.Fatalf("last factor lost: removed=%d refused=%d user=%+v err=%v", removed, refused, u, err)
	}
}

func TestStatementFeedbackConcurrencyAndCompleteExportIntegration(t *testing.T) {
	f := newMultiAllocationFixture(t)
	ctx := t.Context()
	id, err := f.store.CreateStatementShare(ctx, fmt.Sprintf("concurrent-%d", time.Now().UnixNano()), f.neighborID, f.yearID, time.Now().Add(time.Hour), "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	for range 19 {
		if err := f.store.CreateStatementFeedback(ctx, id, f.neighborID, f.yearID, "confirmed", "", decimal.NullDecimal{}, ""); err != nil {
			t.Fatal(err)
		}
	}
	// Make requests contend for the same share. Each waiting writer must count
	// the response committed by its predecessor after obtaining the lock.
	start := make(chan struct{})
	results := make(chan error, 16)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			<-start
			results <- f.store.CreateStatementFeedback(ctx, id, f.neighborID, f.yearID, "confirmed", "", decimal.NullDecimal{}, "")
		})
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, store.ErrFeedbackLimit) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("daily cap accepted %d extra responses, want 1", accepted)
	}
	// Historical responses exceed the UI window but all belong in the export.
	if _, err := f.pool.ExecContext(ctx, `INSERT INTO statement_feedback(share_id,neighbor_id,billing_year_id,status,message,created_at)
		SELECT $1,$2,$3,'disputed','synthetic historic response',now()-interval '2 days' FROM generate_series(1,205)`, id, f.neighborID, f.yearID); err != nil {
		t.Fatal(err)
	}
	ui, err := f.store.ListStatementFeedback(ctx, f.neighborID, f.yearID)
	if err != nil || len(ui) != 200 {
		t.Fatalf("UI feedback: %d %v", len(ui), err)
	}
	all, err := f.store.ListAllStatementFeedback(ctx, f.neighborID, f.yearID)
	if err != nil || len(all) != 225 {
		t.Fatalf("export omitted feedback: %d %v", len(all), err)
	}
}

func TestRateImportPreservesCaseDistinctRigPartsAndOwnCostsIntegration(t *testing.T) {
	st, pool := scratchStore(t)
	ctx := t.Context()
	base, err := st.CreateEmptyBase(ctx, 2020, "Synthetic source")
	if err != nil {
		t.Fatal(err)
	}
	list := store.RateList{}
	for i, name := range []string{"Mower", "mower"} {
		mid, err := st.CreateMachine(ctx, base, name, decimal.NewFromInt(2), decimal.NewFromInt(5), "", i, decimal.NewFromInt(int64(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		var gid int64
		if err := pool.QueryRowContext(ctx, `INSERT INTO gespanne(base_id,name) VALUES($1,$2) RETURNING id`, base, name).Scan(&gid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.ExecContext(ctx, `INSERT INTO gespann_machines(gespann_id,machine_id) VALUES($1,$2)`, gid, mid); err != nil {
			t.Fatal(err)
		}
		list.Machines = append(list.Machines, store.RateMachine{Name: name, Width: decimal.NewFromInt(2), CostPerAB: decimal.NewFromInt(5), Active: true})
	}
	result, err := st.CreateBaseFromRateList(ctx, 2021, "Synthetic import", list, base)
	if err != nil || result.Gespanne != 2 {
		t.Fatalf("import rigs: %+v %v", result, err)
	}
	for i, name := range []string{"Mower", "mower"} {
		var part string
		var cost decimal.Decimal
		if err := pool.QueryRowContext(ctx, `SELECT m.name,m.self_cost_per_h FROM gespanne g JOIN gespann_machines gm ON gm.gespann_id=g.id
			JOIN machines m ON m.id=gm.machine_id WHERE g.base_id=$1 AND g.name=$2`, result.BaseID, name).Scan(&part, &cost); err != nil {
			t.Fatal(err)
		}
		if part != name || !cost.Equal(decimal.NewFromInt(int64(i+1))) {
			t.Fatalf("case-distinct catalog changed: %q -> %q, own cost %s", name, part, cost)
		}
	}
}
