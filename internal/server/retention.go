package server

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/d0linger/treckrr/internal/store"
)

// retentionPath is the retention assistant; the anonymize action returns here.
const retentionPath = "/admin/aufbewahrung"

// retentionYears is the configured period, 7 full calendar years by default.
func (s *Server) retentionYears() int {
	if s.cfg != nil && s.cfg.RetentionYears >= 1 {
		return s.cfg.RetentionYears
	}
	return 7
}

// handleRetention lists archived neighbors whose records have passed the
// retention period, each with the existing anonymization (typed confirmation
// included). Nothing happens without an administrator's click.
func (s *Server) handleRetention(w http.ResponseWriter, r *http.Request) {
	years := s.retentionYears()
	cutoff := store.RetentionCutoff(time.Now(), years)
	candidates, err := s.store.RetentionCandidates(r.Context(), cutoff)
	if err != nil {
		s.serverError(w, "retention: candidates", err)
		return
	}
	data := s.newPage(w, r, "Aufbewahrung", "retention")
	data["Candidates"] = candidates
	data["RetentionYears"] = years
	data["RetentionCutoff"] = cutoff
	s.render(w, r, "retention", data)
}

// retentionNotice reminds administrators once a year while archived
// neighbors could be anonymized.
func (s *Server) retentionNotice(ctx context.Context) *store.NotificationSource {
	now := time.Now()
	candidates, err := s.store.RetentionCandidates(ctx, store.RetentionCutoff(now, s.retentionYears()))
	if err != nil || len(candidates) == 0 {
		return nil
	}
	return &store.NotificationSource{
		DedupeKey: "retention:" + strconv.Itoa(now.Year()), Kind: "privacy", Tone: "info",
		Title:  "Aufbewahrungsfrist abgelaufen: " + strconv.Itoa(len(candidates)) + " archivierte(r) Nachbar(n)",
		Detail: "Die Frist von " + strconv.Itoa(s.retentionYears()) + " vollen Jahren ist vorbei. Bitte prüfen, ob die Daten anonymisiert werden können.",
		Href:   retentionPath, AdminOnly: true,
	}
}
