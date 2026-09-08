package server

import (
	"errors"
	"net/http"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// neighborStat is a neighbor with aggregate counts for the central list.
type neighborStat struct {
	Neighbor models.Neighbor
	Years    int
	Entries  int
}

// handleNeighborsManage renders the central neighbor management page.
func (s *Server) handleNeighborsManage(w http.ResponseWriter, r *http.Request) {
	neighbors, err := s.store.ListNeighbors(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	var stats []neighborStat
	for _, n := range neighbors {
		years, err := s.store.CountYearsForNeighbor(r.Context(), n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		entries, err := s.store.CountEntriesForNeighbor(r.Context(), n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		stats = append(stats, neighborStat{Neighbor: n, Years: years, Entries: entries})
	}

	// Scope (Ausbaukarte 78): archived and anonymised neighbors stood in the same
	// undivided list as the active ones. Default shows the active ones — that is
	// who gets booked — with the other two a click away.
	scope := r.URL.Query().Get("scope")
	shown := make([]neighborStat, 0, len(stats))
	var active, archived, anon int
	for _, st := range stats {
		switch {
		case st.Neighbor.Anonymized:
			anon++
		case st.Neighbor.Archived:
			archived++
		default:
			active++
		}
		keep := false
		switch scope {
		case "archiviert":
			keep = st.Neighbor.Archived && !st.Neighbor.Anonymized
		case "anonymisiert":
			keep = st.Neighbor.Anonymized
		case "alle":
			keep = true
		default:
			keep = !st.Neighbor.Archived && !st.Neighbor.Anonymized
		}
		if keep {
			shown = append(shown, st)
		}
	}
	// The year to link a neighbor into, so the list is a way INTO the data
	// rather than a dead end. Absent on a fresh installation.
	year, yerr := s.store.LatestBillingYear(r.Context())

	data := s.newPage(w, r, "Nachbarn", "neighbors")
	data["Stats"] = shown
	data["Scope"] = scope
	data["CountActive"] = active
	data["CountArchived"] = archived
	data["CountAnon"] = anon
	data["Total"] = len(stats)
	if yerr == nil {
		data["Year"] = year
	}
	s.render(w, r, "neighbors_manage", data)
}

// handleNeighborManageCreate creates a neighbor from the central page.
func (s *Server) handleNeighborManageCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	name := trimmed(r, "name")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		redirect(w, r, "/neighbors")
		return
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		redirect(w, r, "/neighbors")
		return
	}
	note := trimmed(r, "note")
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, "/neighbors")
		return
	}
	id, err := s.store.CreateNeighbor(r.Context(), name, note)
	if err != nil {
		s.setFlash(w, r, "error", "Anlegen fehlgeschlagen (Name bereits vergeben?).")
	} else {
		s.audit(r, "create", "neighbor", id, name)
		s.setFlash(w, r, "success", "Nachbar angelegt.")
	}
	redirect(w, r, "/neighbors")
}

// handleCarryOverNeighbors copies selected neighbors (or all checked ones)
// from the previous billing year into the current one.
func (s *Server) handleCarryOverNeighbors(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	prev, err := s.store.PreviousBillingYear(r.Context(), year.Year)
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Kein Vorjahr vorhanden, aus dem übernommen werden könnte.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	// Restrict the selection to actual members of the previous year.
	prevMembers := map[int64]bool{}
	pm, err := s.store.ListYearNeighbors(r.Context(), prev.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	for _, n := range pm {
		prevMembers[n.ID] = true
	}

	selected, ok := formInt64List(r, "neighbor_ids")
	if !ok {
		s.setFlash(w, r, "error", "Zu viele Nachbarn auf einmal. Bitte in kleineren Gruppen übernehmen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if len(selected) == 0 {
		s.setFlash(w, r, "info", "Keine Nachbarn ausgewählt.")
		redirect(w, r, dashboardURL(yearID))
		return
	}

	added := 0
	for _, id := range selected {
		if !prevMembers[id] {
			continue
		}
		in, err := s.store.NeighborInYear(r.Context(), year.ID, id)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if in {
			continue
		}
		if err := s.store.AddNeighborToYear(r.Context(), year.ID, id); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		added++
	}
	if added == 0 {
		s.setFlash(w, r, "info", "Alle ausgewählten Nachbarn sind bereits enthalten.")
	} else {
		s.audit(r, "carry_over", "year", year.ID, plural(added, "Nachbar", "Nachbarn")+" aus "+itoa(prev.Year))
		s.setFlash(w, r, "success", plural(added, "Nachbar", "Nachbarn")+" aus "+itoa(prev.Year)+" übernommen.")
	}
	redirect(w, r, dashboardURL(yearID))
}

// handleNeighborArchive archives or reactivates a neighbor.
func (s *Server) handleNeighborArchive(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	archived := r.FormValue("archived") == "true"
	name := s.neighborName(r, id)
	if err := s.store.SetNeighborArchived(r.Context(), id, archived); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if archived {
		s.audit(r, "deactivate", "neighbor", id, name)
		s.setFlash(w, r, "success", "Nachbar deaktiviert. Bestehende Buchungen bleiben erhalten.")
	} else {
		s.audit(r, "reactivate", "neighbor", id, name)
		s.setFlash(w, r, "success", "Nachbar wieder aktiviert.")
	}
	redirect(w, r, "/neighbors")
}
