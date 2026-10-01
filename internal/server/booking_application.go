package server

import (
	"context"
	"errors"

	"github.com/d0linger/treckrr/internal/models"
)

var errInvalidBookingDraft = errors.New("booking draft must contain exactly one persistence target")

// bookingCreateResult reports the primary stored row. A zero ID means the
// idempotency key already represented the same booking.
type bookingCreateResult struct {
	MainID int64
}

// createBookingDraft is the application-layer create boundary for the shared
// booking workflow. It selects the existing atomic store adapter without
// merging the intentionally separate own-work and counterclaim models.
func (s *Server) createBookingDraft(ctx context.Context, draft bookingDraft) (bookingCreateResult, error) {
	if (draft.Entry == nil) == (draft.LedgerInput == nil) {
		return bookingCreateResult{}, errInvalidBookingDraft
	}
	if draft.LedgerInput != nil {
		id, err := s.store.CreateLedgerBooking(ctx, *draft.LedgerInput)
		return bookingCreateResult{MainID: id}, err
	}

	helpers := bookingHelpers(draft.Entry, draft.BookedPeople)
	mainID, helperIDs, err := s.store.CreateEntryGroup(ctx, draft.Entry, draft.MachineIDs, helpers)
	if mainID == 0 {
		for _, id := range helperIDs {
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
