package server

import (
	"context"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// bookingCreateResult reports the primary stored row. A zero ID means the
// idempotency key already represented the same booking.
type bookingCreateResult struct {
	MainID int64
}

// createBookingDraft is the application-layer create boundary for the shared
// booking workflow. It selects the existing atomic store adapter without
// merging the intentionally separate own-work and counterclaim models.
func (s *Server) createBookingDraft(ctx context.Context, draft bookingDraft) (bookingCreateResult, error) {
	command := store.BookingCommand{Ledger: draft.LedgerInput}
	if draft.Entry != nil {
		command.Entry = draft.Entry
		command.MachineIDs = draft.MachineIDs
		command.Helpers = bookingHelpers(draft.Entry, draft.BookedPeople)
	}
	result, err := s.store.CreateBooking(ctx, command)
	mainID := result.MainID
	if mainID == 0 {
		for _, id := range result.HelperIDs {
			mainID = max(mainID, id)
		}
	}
	return bookingCreateResult{MainID: mainID}, err
}

// bookingHelpers separates a labor booking's primary person from its companions.
func bookingHelpers(entry *models.Entry, people []models.BookingPerson) []*models.Entry {
	if entry.Unit == models.UnitMannstunde && len(people) > 0 {
		people = people[1:]
	}
	helpers := make([]*models.Entry, 0, len(people))
	for _, person := range people {
		helpers = append(helpers, bookingEntryPerson(person, entry))
	}
	return helpers
}
