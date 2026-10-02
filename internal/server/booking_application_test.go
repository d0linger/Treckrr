package server

import (
	"context"
	"errors"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestCreateBookingDraftRejectsInvalidUnion checks the application boundary
// before any store call: a draft must identify exactly one persistence model.
func TestCreateBookingDraftRejectsInvalidUnion(t *testing.T) {
	server := &Server{}
	for _, draft := range []bookingDraft{
		{},
		{Entry: &models.Entry{}, LedgerInput: &store.LedgerBookingInput{}},
	} {
		if _, err := server.createBookingDraft(context.Background(), draft); !errors.Is(err, store.ErrInvalidBookingCommand) {
			t.Fatalf("createBookingDraft(%+v) error = %v, want %v", draft, err, store.ErrInvalidBookingCommand)
		}
	}
}
