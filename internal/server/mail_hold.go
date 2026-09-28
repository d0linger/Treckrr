package server

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/d0linger/treckrr/internal/store"
)

// heldMailFlash explains why a send request found its intent parked.
const heldMailFlash = "Diese E-Mail wurde nach einer Wiederherstellung angehalten und wird nicht automatisch gesendet. Bitte unter Backup → Angehaltene E-Mails freigeben oder verwerfen."

// heldMailListLimit bounds the held-mail table on the backup page.
const heldMailListLimit = 100

// handleHeldMailRelease returns one held outbox intent to the delivery queue.
func (s *Server) handleHeldMailRelease(w http.ResponseWriter, r *http.Request) {
	s.settleHeldMail(w, r, true)
}

// handleHeldMailDiscard ends one held outbox intent without sending it.
func (s *Server) handleHeldMailDiscard(w http.ResponseWriter, r *http.Request) {
	s.settleHeldMail(w, r, false)
}

// settleHeldMail applies an admin's release/discard decision; the store writes
// the audit line in the same transaction.
func (s *Server) settleHeldMail(w http.ResponseWriter, r *http.Request, release bool) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	var held store.HeldMail
	if release {
		held, err = s.store.ReleaseHeldMail(r.Context(), id)
	} else {
		held, err = s.store.DiscardHeldMail(r.Context(), id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Diese E-Mail ist nicht mehr angehalten.")
	case err != nil:
		slog.Error("held mail decision failed", "id", id, "err", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	case release:
		s.setFlash(w, r, "success", "„"+held.Subject+"“ an "+held.Recipient+" freigegeben — wird beim nächsten Wartungslauf gesendet.")
	default:
		s.setFlash(w, r, "success", "„"+held.Subject+"“ an "+held.Recipient+" verworfen (nicht gesendet).")
	}
	redirect(w, r, "/admin/backup#held-mail")
}
