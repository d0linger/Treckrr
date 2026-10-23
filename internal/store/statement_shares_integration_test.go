//go:build integration

package store_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/store"
)

// TestStatementShareGuardsIntegration pins the store-side guards of statement
// links: only members of the year get a link, expired links resolve to
// nothing, feedback needs a live link of the same account and stops at the
// daily allowance, and anonymizing the neighbor removes links and feedback.
func TestStatementShareGuardsIntegration(t *testing.T) {
	f := newMultiAllocationFixture(t)
	ctx := t.Context()
	future := time.Now().Add(24 * time.Hour)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	expiredHash, liveHash := "expired-"+suffix, "live-"+suffix

	if _, err := f.store.CreateStatementShare(ctx, "outsider-"+suffix, f.neighborID, f.yearID+1_000_000, future, "test"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("link outside the year: err=%v, want ErrNotFound", err)
	}
	if _, err := f.store.CreateStatementShare(ctx, expiredHash, f.neighborID, f.yearID, time.Now().Add(-time.Minute), "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := f.store.ResolveStatementShare(ctx, expiredHash); err != nil || ok {
		t.Fatalf("expired link resolved: ok=%v err=%v", ok, err)
	}
	id, err := f.store.CreateStatementShare(ctx, liveHash, f.neighborID, f.yearID, future, "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, nID, yID, ok, err := f.store.ResolveStatementShare(ctx, liveHash); err != nil || !ok || nID != f.neighborID || yID != f.yearID {
		t.Fatalf("live link: n=%d y=%d ok=%v err=%v", nID, yID, ok, err)
	}
	if err := f.store.CreateStatementFeedback(ctx, id, f.neighborID, f.yearID+1, "confirmed", "", decimal.NullDecimal{}, ""); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("feedback for another account: err=%v, want ErrNotFound", err)
	}
	amount := decimal.NullDecimal{Decimal: decimal.RequireFromString("12.50"), Valid: true}
	for i := range 20 {
		if err := f.store.CreateStatementFeedback(ctx, id, f.neighborID, f.yearID, "disputed", "Zeile", amount, "Einwand"); err != nil {
			t.Fatalf("feedback %d: %v", i, err)
		}
	}
	if err := f.store.CreateStatementFeedback(ctx, id, f.neighborID, f.yearID, "confirmed", "", decimal.NullDecimal{}, ""); !errors.Is(err, store.ErrFeedbackLimit) {
		t.Fatalf("21st feedback: err=%v, want ErrFeedbackLimit", err)
	}
	feedback, err := f.store.ListStatementFeedback(ctx, f.neighborID, f.yearID)
	if err != nil || len(feedback) != 20 || !feedback[0].LineAmount.Decimal.Equal(amount.Decimal) {
		t.Fatalf("feedback=%d err=%v", len(feedback), err)
	}

	if err := f.store.AnonymizeNeighbor(ctx, f.neighborID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := f.store.ResolveStatementShare(ctx, liveHash); err != nil || ok {
		t.Fatalf("link survived anonymization: ok=%v err=%v", ok, err)
	}
	if left, err := f.store.ListStatementFeedback(ctx, f.neighborID, f.yearID); err != nil || len(left) != 0 {
		t.Fatalf("feedback survived anonymization: %d err=%v", len(left), err)
	}
}
