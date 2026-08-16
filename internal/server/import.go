package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// importToken returns a random id tying a preview to its commit. Each imported
// row is keyed <token>:<line>, so a re-submitted commit (same token) dedupes every
// row to a no-op via CreateEntry's ON CONFLICT, while two genuinely identical CSV
// rows (different lines) still both import. Falls back to "" (no dedup) if the RNG
// fails — worst case reverts to the prior non-idempotent behaviour.
func importToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

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
	// Strip a leading UTF-8 BOM. Both our own CSV export and Excel/LibreOffice
	// write one; left in place it prefixes the first cell (BOM + "Nachbar"), so
	// the header-skip check below misses and the header parses as a bad data row.
	text = strings.TrimPrefix(text, "\uFEFF")
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

// markLockedRows rejects any importable row whose neighbour already has a
// festgeschriebene (issued) invoice for the year — mirroring the invoiceLocked
// guard that the single-entry create enforces (§131/Festschreibung). Without it a
// bulk import would silently add bookings to a frozen invoice basis. Looked up
// once per distinct neighbour; a non-"not found" store error fails closed (locked).
func (s *Server) markLockedRows(ctx context.Context, yearID int64, rows []importRow) {
	locked := make(map[int64]bool)
	for i := range rows {
		if !rows[i].OK() {
			continue
		}
		nid := rows[i].NeighborID
		if _, seen := locked[nid]; !seen {
			_, err := s.store.GetInvoice(ctx, yearID, nid)
			locked[nid] = !errors.Is(err, store.ErrNotFound) // invoice present (or lookup failed) → locked
		}
		if locked[nid] {
			rows[i].Err = "Rechnung festgeschrieben"
		}
	}
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

// handleImportSample streams a small, correctly-formatted example CSV so a user
// can see the exact import layout (same semicolon columns as the export) and fill
// it in. It is a static template — no year or DB access — and mirrors the export's
// UTF-8 BOM + ';' delimiter + German decimals so a real exported file and this
// sample parse identically. The placeholder neighbours won't import until renamed
// to actual year members; that's intentional (the file is a format guide).
func (s *Server) handleImportSample(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"treckrr_import_vorlage.csv\"")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM, so Excel opens umlauts/€ correctly
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	defer cw.Flush()
	_ = cw.Write([]string{
		"Nachbar", "Datum", "Tätigkeit", "Traktor", "Belastung", "Maschinen",
		"Einheit", "Menge", "Satz/Einheit (€)", "Kosten (€)", "Notiz",
	})
	// Rig columns (Traktor/Belastung/Maschinen) and Kosten are ignored on import;
	// they stay for column alignment with the export. Cost is recomputed Menge×Satz.
	for _, row := range [][]string{
		{"Max Mustermann", "2026-03-14", "Ballenpressen", "", "", "", "Ballen", "10", "3,20", "32,00", "Beispiel: 10 × 3,20"},
		{"Max Mustermann", "2026-04-02", "Mähen", "", "", "", "h", "4,5", "28,00", "126,00", "Einheit leer = Stunden"},
		{"Anna Beispiel", "15.05.2026", "Transport", "", "", "", "km", "120", "0,90", "108,00", "Datum auch TT.MM.JJJJ"},
	} {
		_ = cw.Write(row)
	}
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
	s.markLockedRows(r.Context(), yearID, rows)
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
	data["ImportToken"] = importToken() // one-shot: a re-submitted commit becomes a no-op
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
	// Re-check locks at commit time (not just preview): an invoice may have been
	// festgeschrieben between preview and commit. Locked rows get an error and are
	// skipped by the !row.OK() guard below.
	s.markLockedRows(r.Context(), yearID, rows)
	// One-shot import: keying each row <token>:<line> makes a re-submitted commit
	// (double-click, browser retry) a no-op via CreateEntry's ON CONFLICT, without
	// deduping two genuinely identical rows in the same file (distinct lines).
	token := trimmed(r, "import_token")
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
		if token != "" {
			e.IdempotencyKey = token + ":" + strconv.Itoa(row.Line)
		}
		if row.Unit == "h" { // keep the hour-booking convention so it counts as hours
			e.Hours = row.Qty
			e.HourlyRate = row.Price
		}
		newID, err := s.store.CreateEntry(r.Context(), e, nil)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if newID != 0 { // 0 = an already-imported row on a re-submit; don't double-count
			created++
		}
	}
	s.audit(r, "import", "year", yearID, itoa(created)+" Buchungen importiert")
	s.setFlash(w, r, "success", itoa(created)+" Buchung(en) importiert.")
	redirect(w, r, dashboardURL(yearID))
}
