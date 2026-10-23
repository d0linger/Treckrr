//go:build integration

package store_test

import (
	"context"
	"testing"
	"time"
)

// TestSecurityPolicyGraceRestartIntegration pins when the grace period starts
// over: a newly required role restarts it, saving the same roles keeps it, and
// requiring nothing clears it. The singleton row is restored afterwards.
func TestSecurityPolicyGraceRestartIntegration(t *testing.T) {
	f := newMultiAllocationFixture(t)
	ctx := t.Context()
	t.Cleanup(func() {
		// t.Context is already canceled when cleanups run.
		if _, err := f.store.UpdateSecurityPolicy(context.Background(), false, false, 7); err != nil {
			t.Errorf("reset policy: %v", err)
		}
	})
	p, err := f.store.UpdateSecurityPolicy(ctx, true, false, 7)
	if err != nil || p.EnforcedSince == nil || !p.RequireAdmin {
		t.Fatalf("enable admins: %+v err=%v", p, err)
	}
	first := *p.EnforcedSince
	time.Sleep(20 * time.Millisecond)
	if p, err = f.store.UpdateSecurityPolicy(ctx, true, false, 14); err != nil || p.EnforcedSince == nil || !p.EnforcedSince.Equal(first) || p.GraceDays != 14 {
		t.Fatalf("same roles must keep the start: %+v err=%v", p, err)
	}
	if p, err = f.store.UpdateSecurityPolicy(ctx, true, true, 14); err != nil || p.EnforcedSince == nil || !p.EnforcedSince.After(first) {
		t.Fatalf("a newly required role must restart the grace period: %+v err=%v", p, err)
	}
	if p, err = f.store.UpdateSecurityPolicy(ctx, false, false, 14); err != nil || p.EnforcedSince != nil {
		t.Fatalf("no requirement must clear the start: %+v err=%v", p, err)
	}
	if got, err := f.store.GetSecurityPolicy(ctx); err != nil || got.RequireAdmin || got.RequireEditor {
		t.Fatalf("read back: %+v err=%v", got, err)
	}
}
