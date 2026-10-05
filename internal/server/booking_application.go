package server

import (
	"context"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// bookingCreateResult reports which parts of a booking were newly stored.
type bookingCreateResult struct {
	MainID    int64
	HelperIDs []int64
}

// hasCreatedRows reports whether the command stored a main or helper row.
func (r bookingCreateResult) hasCreatedRows() bool {
	return r.MainID != 0 || len(r.HelperIDs) != 0
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
	return bookingCreateResult{MainID: result.MainID, HelperIDs: result.HelperIDs}, err
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
