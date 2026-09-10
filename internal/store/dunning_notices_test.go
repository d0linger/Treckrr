package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

func TestNegativeDunningFeesRejectedBeforeDatabaseAccess(t *testing.T) {
	st := store.New(nil, "test-secret")
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "first company fee", run: func() error {
			return st.UpdateCompany(ctx, models.Company{DunningFee1: dec("-1")})
		}},
		{name: "second company fee", run: func() error {
			return st.UpdateCompany(ctx, models.Company{DunningFee2: dec("-1")})
		}},
		{name: "notice fee", run: func() error {
			return st.RecordDunningNotice(ctx, store.DunningNotice{Fee: dec("-1")})
		}},
		{name: "queued notice fee", run: func() error {
			return st.EnqueueMail(ctx, store.OutboxMail{Meta: store.OutboxMeta{Fee: dec("-1")}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, store.ErrNegativeDunningFee) {
				t.Fatalf("negative fee = %v, want ErrNegativeDunningFee", err)
			}
		})
	}
}
