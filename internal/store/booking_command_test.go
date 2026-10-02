package store

import (
	"errors"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

func TestCreateBookingRejectsInvalidCommandsBeforeStoreAccess(t *testing.T) {
	tests := []struct {
		name    string
		command BookingCommand
	}{
		{name: "empty command"},
		{
			name: "mixed persistence targets",
			command: BookingCommand{
				Entry:  &models.Entry{},
				Ledger: &LedgerBookingInput{},
			},
		},
		{
			name: "ledger with entry helpers",
			command: BookingCommand{
				Ledger:  &LedgerBookingInput{},
				Helpers: []*models.Entry{{}},
			},
		},
		{
			name: "multi helper custom audit",
			command: BookingCommand{
				Entry:   &models.Entry{},
				Helpers: []*models.Entry{{}, {}},
				Audit:   &EntryAudit{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&Store{}).CreateBooking(t.Context(), tt.command)
			if !errors.Is(err, ErrInvalidBookingCommand) {
				t.Fatalf("CreateBooking() error = %v, want ErrInvalidBookingCommand", err)
			}
		})
	}
}
