package server

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
	"treckrr/internal/web"
)

// sanitizeFilename reduces a user-supplied name to characters safe inside a
// quoted Content-Disposition filename: Unicode letters/digits and . - _ are
// kept, everything else (spaces, quotes, backslashes, path separators, control
// chars, header-param punctuation) becomes an underscore. Prevents a name like
// `a"; …` from breaking the header's quoting. Empty/stripped input falls back
// to "export".
func sanitizeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "._")
	if out == "" {
		return "export"
	}
	return out
}

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
		s.serverError(w, r.URL.Path, err)
		return
	}
	names, err := s.neighborNames(r)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
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
		s.serverError(w, r.URL.Path, err)
		return
	}
	names := map[int64]string{neighbor.ID: neighbor.Name}
	filename := fmt.Sprintf("treckrr_%s_%d.csv", sanitizeFilename(neighbor.Name), year.Year)
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

	var total decimal.Decimal
	for _, e := range entries {
		_ = cw.Write([]string{
			csvSafe(names[e.NeighborID]),
			web.Date(e.Date),
			csvSafe(e.TaskLabel),
			csvSafe(e.TractorLabel),
			csvSafe(e.LoadLabel),
			csvSafe(e.MachineLabels),
			deDecimal(e.Hours),
			deDecimal(e.HourlyRate),
			deDecimal(e.Cost),
			csvSafe(e.Note),
		})
		total = total.Add(e.Cost)
	}
	_ = cw.Write([]string{"", "", "", "", "", "", "", "Gesamt", deDecimal(total), ""})
}

// deDecimal formats an exact decimal with a comma separator for German
// spreadsheets, e.g. 1234.5 -> "1234,50".
func deDecimal(v decimal.Decimal) string {
	return strings.Replace(v.StringFixed(2), ".", ",", 1)
}

// csvSafe neutralizes spreadsheet formula injection: a cell whose first
// character is one of = + - @ (or a leading tab/CR that some parsers strip to
// reveal such a character) is prefixed with a single quote so Excel/LibreOffice
// treat it as literal text instead of a formula. Applied to user-entered text
// columns only, never to the numeric columns.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	switch v[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + v
	}
	return v
}
