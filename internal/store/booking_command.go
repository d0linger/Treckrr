package store

import (
	"context"
	"errors"

	"github.com/d0linger/treckrr/internal/models"
)

// ErrInvalidBookingCommand reports a command that mixes persistence targets or
// supplies fields that cannot be handled atomically by the selected target.
var ErrInvalidBookingCommand = errors.New("store: invalid booking command")

// BookingCommand is the shared persistence boundary for booking input adapters.
// Exactly one of Entry and Ledger must be set. The command deliberately retains
// separate storage models for own work and signed neighbor counterclaims.
type BookingCommand struct {
	Entry      *models.Entry
	MachineIDs []int64
	Helpers    []*models.Entry
	Ledger     *LedgerBookingInput
	Audit      *EntryAudit
}

// BookingResult identifies rows inserted by one command. Zero IDs denote
// idempotent replays of rows that were already stored.
type BookingResult struct {
	MainID    int64
	HelperIDs []int64
}

func (cmd BookingCommand) validate() error {
	if (cmd.Entry == nil) == (cmd.Ledger == nil) {
		return ErrInvalidBookingCommand
	}
	if cmd.Ledger != nil {
		if len(cmd.MachineIDs) != 0 || len(cmd.Helpers) != 0 || cmd.Audit != nil {
			return ErrInvalidBookingCommand
		}
		return nil
	}
	if len(cmd.Helpers) > 1 && cmd.Audit != nil {
		return ErrInvalidBookingCommand
	}
	return nil
}

// CreateBooking dispatches one validated command to the existing atomic store
// operation. It centralizes adapter persistence without changing account locks,
// idempotency, audit behavior, or the underlying accounting tables.
func (s *Store) CreateBooking(ctx context.Context, cmd BookingCommand) (BookingResult, error) {
	if err := cmd.validate(); err != nil {
		return BookingResult{}, err
	}
	if cmd.Ledger != nil {
		id, err := s.CreateLedgerBooking(ctx, *cmd.Ledger)
		return BookingResult{MainID: id}, err
	}

	switch len(cmd.Helpers) {
	case 0:
		id, err := s.CreateEntryAudited(ctx, cmd.Entry, cmd.MachineIDs, cmd.Audit)
		return BookingResult{MainID: id, HelperIDs: []int64{}}, err
	case 1:
		id, helperID, err := s.CreateEntryPairAudited(
			ctx,
			cmd.Entry,
			cmd.MachineIDs,
			cmd.Helpers[0],
			cmd.Audit,
		)
		return BookingResult{MainID: id, HelperIDs: []int64{helperID}}, err
	default:
		id, helperIDs, err := s.CreateEntryGroup(ctx, cmd.Entry, cmd.MachineIDs, cmd.Helpers)
		return BookingResult{MainID: id, HelperIDs: helperIDs}, err
	}
}
