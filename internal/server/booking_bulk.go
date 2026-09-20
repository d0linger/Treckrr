package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/d0linger/treckrr/internal/store"
)

// formBookingRefs accepts explicit source IDs while retaining old entry forms.
func formBookingRefs(r *http.Request) ([]store.BookingRef, bool) {
	values := r.Form["booking_id"]
	legacy := r.Form["entry_id"]
	if len(values)+len(legacy) > maxFormListLen {
		return nil, false
	}
	refs := []store.BookingRef{}
	seen := map[store.BookingRef]bool{}
	appendRef := func(source, raw string) bool {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 || (source != "entry" && source != "ledger") {
			return false
		}
		ref := store.BookingRef{Source: source, ID: id}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
		return true
	}
	for _, value := range values {
		source, raw, found := strings.Cut(value, ":")
		if !found || !appendRef(source, raw) {
			return nil, false
		}
	}
	for _, raw := range legacy {
		if !appendRef("entry", raw) {
			return nil, false
		}
	}
	return refs, true
}
