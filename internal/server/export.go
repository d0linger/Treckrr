package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"treckrr/internal/models"
	"treckrr/internal/web"
)

// handleExportYear exports all entries of a billing year as CSV.
func (s *Server) handleExportYear(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entries, err := s.store.ListEntriesByYear(r.Context(), year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	names, err := s.neighborNames(r)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	filename := fmt.Sprintf("treckrr_%d.csv", year.Year)
	s.writeCSV(w, filename, entries, names)
}

// handleExportNeighbor exports one neighbor's entries within a billing year.
func (s *Server) handleExportNeighbor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	entries, err := s.store.ListEntries(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	names := map[int64]string{neighbor.ID: neighbor.Name}
	safeName := strings.ReplaceAll(neighbor.Name, " ", "_")
	filename := fmt.Sprintf("treckrr_%s_%d.csv", safeName, year.Year)
	s.writeCSV(w, filename, entries, names)
}

func (s *Server) neighborNames(r *http.Request) (map[int64]string, error) {
	neighbors, err := s.store.ListNeighbors(r.Context())
	if err != nil {
		return nil, err
	}
	m := make(map[int64]string, len(neighbors))
	for _, n := range neighbors {
		m[n.ID] = n.Name
	}
	return m, nil
}

// writeCSV renders entries as a German-locale, semicolon-separated CSV that
// opens cleanly in Excel/LibreOffice.
func (s *Server) writeCSV(w http.ResponseWriter, filename string, entries []models.Entry, names map[int64]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")

	// UTF-8 BOM so Excel detects the encoding and shows umlauts correctly.
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	cw.Comma = ';'
	defer cw.Flush()

	_ = cw.Write([]string{
		"Nachbar", "Datum", "Tätigkeit", "Traktor", "Belastung",
		"Maschinen", "Stunden", "Stundensatz (€)", "Kosten (€)", "Notiz",
	})

	var total float64
	for _, e := range entries {
		_ = cw.Write([]string{
			names[e.NeighborID],
			web.Date(e.Date),
			e.TaskLabel,
			e.TractorLabel,
			e.LoadLabel,
			e.MachineLabels,
			deDecimal(e.Hours),
			deDecimal(e.HourlyRate),
			deDecimal(e.Cost),
			e.Note,
		})
		total += e.Cost
	}
	_ = cw.Write([]string{"", "", "", "", "", "", "", "Gesamt", deDecimal(total), ""})
}

// deDecimal formats with a comma decimal separator for German spreadsheets.
func deDecimal(v float64) string {
	return strings.Replace(strconv.FormatFloat(v, 'f', 2, 64), ".", ",", 1)
}
