package server

import (
	"encoding/csv"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// importRow is one parsed CSV line for the booking import, with a per-row error
// (empty = importable). Columns mirror the CSV export layout; the rig columns
// (Traktor/Belastung/Maschinen) and the Kosten column are ignored — cost is
// recomputed as Menge × Satz so the import can't disagree with the app's math.
type importRow struct {
	Line       int
	NeighborID int64
	Neighbor   string
	Date       time.Time
	DateStr    string
	Task       string
	Unit       string
	Qty        decimal.Decimal
	Price      decimal.Decimal
	Cost       decimal.Decimal
	Note       string
	Err        string
}

func (row importRow) OK() bool { return row.Err == "" }

// parseImportDate accepts the export's ISO date and the common German format.
func parseImportDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02", "02.01.2006"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// col returns the trimmed field at index i, or "" if the row is shorter.
func col(rec []string, i int) string {
	if i < len(rec) {
		return strings.TrimSpace(rec[i])
	}
	return ""
}

// parseImportCSV parses the semicolon CSV into rows, resolving each neighbor
// against the year's members. It never touches the database beyond the caller's
// prebuilt member map, so it is safe to run for the dry-run preview.
func parseImportCSV(text string, members map[string]int64) ([]importRow, error) {
	cr := csv.NewReader(strings.NewReader(text))
	cr.Comma = ';'
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	var out []importRow
	line := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		line++
		// Skip the header row and blank lines.
		if line == 1 && strings.EqualFold(col(rec, 0), "Nachbar") {
			continue
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		row := importRow{
			Line: line, Neighbor: col(rec, 0), DateStr: col(rec, 1), Task: col(rec, 2),
			Unit: col(rec, 6), Qty: parseGermanDecimal(col(rec, 7)),
			Price: parseGermanDecimal(col(rec, 8)), Note: col(rec, 10),
		}
		if row.Unit == "" {
			row.Unit = "h"
		}
		row.Cost = row.Qty.Mul(row.Price).Round(2)

		switch {
		case row.Neighbor == "":
			row.Err = "Nachbar fehlt"
		case members[strings.ToLower(row.Neighbor)] == 0:
			row.Err = "Nachbar nicht im Jahr"
		case !row.Qty.IsPositive():
			row.Err = "Menge muss > 0 sein"
		case !row.Price.IsPositive():
			row.Err = "Satz muss > 0 sein"
		default:
			if t, ok := parseImportDate(row.DateStr); ok {
				row.Date = t
			} else {
				row.Err = "Datum ungültig (JJJJ-MM-TT)"
			}
		}
		if row.OK() {
			row.NeighborID = members[strings.ToLower(row.Neighbor)]
		}
		out = append(out, row)
	}
	return out, nil
}

// yearMembers builds a lower-cased name → id map of the year's neighbors, so the
// import can only target existing members (no silent neighbor creation).
func (s *Server) yearMembers(r *http.Request, yearID int64) (map[string]int64, error) {
	ns, err := s.store.ListYearNeighbors(r.Context(), yearID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]int64, len(ns))
	for _, n := range ns {
		m[strings.ToLower(strings.TrimSpace(n.Name))] = n.ID
	}
	return m, nil
}

// handleImportForm renders the upload form for a billing year.
func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	data := s.newPage(w, r, "Buchungen importieren", "dashboard")
	data["Year"] = year
	s.render(w, r, "import", data)
}

// handleImportPreview parses the uploaded CSV and shows a dry-run: which rows
// would import and which are rejected. Nothing is written. The raw CSV is echoed
// back in a hidden field so the commit step re-parses the exact same input.
func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := formInt64(r, "year_id")
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.setFlash(w, r, "error", "Bitte eine CSV-Datei wählen.")
		redirect(w, r, "/entries/import?year="+itoa64(yearID))
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	members, err := s.yearMembers(r, yearID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	rows, perr := parseImportCSV(string(raw), members)
	if perr != nil {
		s.setFlash(w, r, "error", "CSV konnte nicht gelesen werden.")
		redirect(w, r, "/entries/import?year="+itoa64(yearID))
		return
	}
	okCount := 0
	for _, row := range rows {
		if row.OK() {
			okCount++
		}
	}
	data := s.newPage(w, r, "Import-Vorschau", "dashboard")
	data["Year"] = year
	data["Rows"] = rows
	data["OKCount"] = okCount
	data["Total"] = len(rows)
	data["CSV"] = string(raw)
	s.render(w, r, "import", data)
}

// handleImportCommit re-parses the echoed CSV and creates the importable rows.
func (s *Server) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := formInt64(r, "year_id")
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	members, err := s.yearMembers(r, yearID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	rows, perr := parseImportCSV(r.FormValue("csv"), members)
	if perr != nil {
		s.setFlash(w, r, "error", "CSV konnte nicht gelesen werden.")
		redirect(w, r, "/entries/import?year="+itoa64(yearID))
		return
	}
	created := 0
	for _, row := range rows {
		if !row.OK() {
			continue
		}
		e := &models.Entry{
			NeighborID: row.NeighborID, BillingYearID: yearID, Date: row.Date,
			TaskLabel: row.Task, Note: row.Note, Unit: row.Unit,
			Quantity: row.Qty, UnitPrice: row.Price, Cost: row.Cost,
		}
		if row.Unit == "h" { // keep the hour-booking convention so it counts as hours
			e.Hours = row.Qty
			e.HourlyRate = row.Price
		}
		if _, err := s.store.CreateEntry(r.Context(), e, nil); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		created++
	}
	s.audit(r, "import", "year", yearID, itoa(created)+" Buchungen importiert")
	s.setFlash(w, r, "success", itoa(created)+" Buchung(en) importiert.")
	redirect(w, r, dashboardURL(yearID))
}
