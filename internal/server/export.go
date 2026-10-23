package server

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
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
		s.notFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
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
	s.writeExport(w, r, fmt.Sprintf("treckrr_%d", year.Year), entriesTable(fmt.Sprintf("Buchungen %d", year.Year), entries, names))
}

// handleExportNeighbor exports one neighbor's entries within a billing year.
func (s *Server) handleExportNeighbor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
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
	s.writeExport(w, r, fmt.Sprintf("treckrr_%s_%d", sanitizeFilename(neighbor.Name), year.Year),
		entriesTable(fmt.Sprintf("Buchungen %d", year.Year), entries, names))
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

// csvDownload is the one CSV response preamble: content type, attachment
// name, UTF-8 BOM (Excel needs it for umlauts), semicolon delimiter — a wire
// contract with Excel/LibreOffice that had drifted across six hand-written
// copies (one was not even deferred, so an early return skipped the flush).
// Defer the returned finish; it flushes and logs a truncated download, since
// the HTTP status is long gone by then.
func csvDownload(w http.ResponseWriter, r *http.Request, filename string) (*csv.Writer, func()) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	return cw, func() {
		cw.Flush()
		if err := cw.Error(); err != nil {
			slog.Warn("csv export incomplete", "path", sanitizeLog(r.URL.Path), "err", sanitizeLog(err.Error()))
		}
	}
}

// entriesTable is the bookings export (year or one neighbor): the same columns
// as CSV and XLSX, with a total row.
func entriesTable(sheet string, entries []models.Entry, names map[int64]string) exportTable {
	t := exportTable{sheet: sheet, header: []string{
		"Nachbar", "Datum", "Tätigkeit", "Traktor", "Belastung",
		"Maschinen", "Einheit", "Menge", "Satz/Einheit (€)", "Kosten (€)", "Notiz",
	}}
	var total decimal.Decimal
	for _, e := range entries {
		// Skip voided (stornierte) bookings so the export matches every other
		// total in the app, which all sum with `AND NOT voided`.
		if e.Voided {
			continue
		}
		t.add(cellText(names[e.NeighborID]), cellDate(e.Date), cellText(e.TaskLabel), cellText(e.TractorLabel),
			cellText(e.LoadLabel), cellText(e.MachineLabels), cellText(e.Unit), cellNumber(e.Quantity),
			cellMoney(e.UnitPrice), cellMoney(e.Cost), cellText(e.Note))
		total = total.Add(e.Cost)
	}
	empty := cellEmpty()
	t.add(empty, empty, empty, empty, empty, empty, empty, empty, cellLabel("Gesamt"), cellMoney(total), empty)
	return t
}

// deDecimal formats an exact decimal with a comma separator for German
// spreadsheets, e.g. 1234.5 -> "1234,50".
func deDecimal(v decimal.Decimal) string {
	return strings.Replace(v.StringFixed(2), ".", ",", 1)
}

// csvSafe neutralizes spreadsheet formula injection: a cell whose first
// non-whitespace character is one of = + - @ is prefixed with a single quote so
// Excel/LibreOffice treat it as literal text instead of a formula. All leading
// Unicode whitespace is skipped first (space, tab, CR/LF, vertical tab, form
// feed, NBSP, …), because several importers strip it before evaluating a formula
// (so " =X" or " =X" are dangerous too). Applied to user-entered text
// columns only, never to the numeric columns.
// Keep literal percent/pipe prefixes unchanged: they are not documented formula
// starters in Excel/Calc; a pipe inside a formula is covered by its leading trigger.
func csvSafe(v string) string {
	if v == "" {
		return v
	}
	t := strings.TrimLeftFunc(v, unicode.IsSpace)
	if t == "" {
		return v
	}
	switch t[0] {
	case '=', '+', '-', '@':
		return "'" + v
	}
	return v
}
