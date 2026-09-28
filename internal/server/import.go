package server

import (
	"context"
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

// The import token ties a preview to its commit twice over: it names the CSV
// parked server-side by the preview (store.SaveImportUpload), and each imported
// row is keyed <token>:<line>, so a re-submitted commit (same token) dedupes
// every row to a no-op via CreateEntry's ON CONFLICT, while two genuinely
// identical CSV rows (different lines) still both import.

// Numeric bounds of the entries columns an imported row is written to. Postgres
// rejects a value past a NUMERIC column's precision and silently rounds excess
// scale, so both are checked per row before anything is written: an overflow on
// row N used to fail the commit after rows 1..N-1 were stored.
var (
	maxImportQty   = decimal.NewFromInt(1_000_000_000)  // quantity/unit_price NUMERIC(14,4), kept at the form's 1e9 cap
	maxImportHours = decimal.NewFromInt(10_000_000)     // hours NUMERIC(10,3)
	maxImportRate  = decimal.NewFromInt(100_000_000)    // hourly_rate NUMERIC(12,4)
	maxImportCost  = decimal.NewFromInt(10_000_000_000) // cost NUMERIC(14,4)
)

// importAmountError validates a parsed row's quantity, rate and derived cost
// against the database columns. Returns "" when the row fits.
func importAmountError(qtyOK, priceOK bool, row importRow) string {
	switch {
	case !qtyOK || !row.Qty.IsPositive():
		return "Menge muss > 0 sein"
	case !priceOK || !row.Price.IsPositive():
		return "Satz muss > 0 sein"
	case !row.Qty.Equal(row.Qty.Truncate(4)):
		return "Menge: höchstens 4 Nachkommastellen"
	case !row.Price.Equal(row.Price.Truncate(4)):
		return "Satz: höchstens 4 Nachkommastellen"
	case row.Qty.GreaterThanOrEqual(maxImportQty):
		return "Menge zu groß"
	case row.Price.GreaterThanOrEqual(maxImportQty):
		return "Satz zu groß"
	case row.Unit == "h" && !row.Qty.Equal(row.Qty.Truncate(3)):
		return "Stunden: höchstens 3 Nachkommastellen"
	case row.Unit == "h" && row.Qty.GreaterThanOrEqual(maxImportHours):
		return "Stunden zu groß"
	case row.Unit == "h" && row.Price.GreaterThanOrEqual(maxImportRate):
		return "Stundensatz zu groß"
	case row.Cost.GreaterThanOrEqual(maxImportCost):
		return "Kosten (Menge × Satz) zu groß"
	}
	return ""
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
		// Skip the header row (identified by its "Nachbar" first column, wherever
		// it lands — e.g. after a leading blank line, so a real export with a stray
		// empty first record still parses) and any whitespace-only record.
		if strings.EqualFold(col(rec, 0), "Nachbar") {
			continue
		}
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		qty, qtyOK := parseGermanDecimalOK(col(rec, 7))
		price, priceOK := parseGermanDecimalOK(col(rec, 8))
		row := importRow{
			Line: line, Neighbor: col(rec, 0), DateStr: col(rec, 1), Task: col(rec, 2),
			Unit: col(rec, 6), Qty: qty, Price: price, Note: col(rec, 10),
		}
		if row.Unit == "" {
			row.Unit = "h"
		}
		row.Cost = row.Qty.Mul(row.Price).Round(2)
		amountErr := importAmountError(qtyOK, priceOK, row)

		switch {
		case row.Neighbor == "":
			row.Err = "Nachbar fehlt"
		case members[strings.ToLower(row.Neighbor)] == 0:
			row.Err = "Nachbar nicht im Jahr"
		case amountErr != "":
			row.Err = amountErr
		case lenError("Tätigkeit", row.Task, maxNameLen) != "":
			row.Err = lenError("Tätigkeit", row.Task, maxNameLen)
		case lenError("Einheit", row.Unit, 16) != "":
			row.Err = lenError("Einheit", row.Unit, 16)
		case lenError("Notiz", row.Note, maxNoteLen) != "":
			row.Err = lenError("Notiz", row.Note, maxNoteLen)
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

// markLockedRows rejects any importable row whose neighbor already has a
// festgeschriebene (issued) invoice for the year — mirroring the invoiceLocked
// guard that the single-entry create enforces (§131/Festschreibung). Without it a
// bulk import would silently add bookings to a frozen invoice basis. Looked up
// once per distinct neighbor; a non-"not found" store error fails closed (locked).
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
// sample parse identically. The placeholder neighbors won't import until renamed
// to actual year members; that's intentional (the file is a format guide).
func (s *Server) handleImportSample(w http.ResponseWriter, r *http.Request) {
	cw, finish := csvDownload(w, r, "treckrr_import_vorlage.csv")
	defer finish()
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
// would import and which are rejected. No booking is written. The CSV is parked
// server-side under the import token, so the commit re-parses the exact same
// input without the browser posting the file back (which, percent-encoded,
// broke the body cap for medium-sized files).
func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	// The correction editor (Ausbaukarte 69) posts a plain urlencoded form, the
	// upload a multipart one. ParseMultipartForm parses the urlencoded body too
	// and then reports ErrNotMultipart, which is not an error here.
	if err := r.ParseMultipartForm(maxImportPayloadLen); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.setFlash(w, r, "error", msgImportTooLarge)
			redirect(w, r, "/entries/import?year="+itoa64(formInt64(r, "year_id")))
			return
		}
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	yearID := formInt64(r, "year_id")
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	// Two ways in: a fresh upload, or the corrected text from the preview's own
	// editor (Ausbaukarte 69) — fixing a typo should not mean editing the file
	// on disk and uploading it again.
	var raw []byte
	if edited := r.FormValue("csv"); strings.TrimSpace(edited) != "" {
		if len(edited) > maxImportPayloadLen {
			s.setFlash(w, r, "error", msgImportTooLarge)
			redirect(w, r, "/entries/import?year="+itoa64(yearID))
			return
		}
		raw = []byte(edited)
	} else {
		file, _, err := r.FormFile("file")
		if err != nil {
			s.setFlash(w, r, "error", "Bitte eine CSV-Datei wählen.")
			redirect(w, r, "/entries/import?year="+itoa64(yearID))
			return
		}
		defer file.Close()
		var fits bool
		raw, fits, err = readImportFile(file)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if !fits {
			s.setFlash(w, r, "error", msgImportTooLarge)
			redirect(w, r, "/entries/import?year="+itoa64(yearID))
			return
		}
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
	// One-shot: the token names this exact CSV, and a re-submitted commit with
	// it becomes a no-op row by row.
	token, err := s.store.SaveImportUpload(r.Context(), store.ImportUploadBooking, s.currentUserID(r), yearID, raw)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Import-Vorschau", "dashboard")
	data["Year"] = year
	data["Rows"] = rows
	data["OKCount"] = okCount
	data["Total"] = len(rows)
	data["CSV"] = string(raw) // the correction editor's starting text only
	data["ImportToken"] = token
	s.render(w, r, "import", data)
}

// importCommitSkipReason classifies a per-row CreateEntry failure. A business
// refusal concerns that row alone and is reported; ok=false means an
// infrastructure error that aborts the commit.
func importCommitSkipReason(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrInvoiceLocked):
		return "Rechnung festgeschrieben", true
	case errors.Is(err, store.ErrNeighborAnonymized):
		return "Nachbar anonymisiert", true
	case errors.Is(err, store.ErrYearCompleted):
		return "Abrechnungsjahr abgeschlossen", true
	case errors.Is(err, store.ErrNotFound):
		return "Nachbar nicht im Jahr", true
	case errors.Is(err, store.ErrIdempotencyConflict):
		return "bereits für einen anderen Nachbarn importiert", true
	}
	return "", false
}

// importCommitSummary builds the audit detail and the flash text of a commit.
func importCommitSummary(created, deselected int, failed []string) (audit, flash string) {
	audit = itoa(created) + " Buchungen importiert"
	flash = itoa(created) + " Buchung(en) importiert."
	if deselected > 0 {
		audit += ", " + itoa(deselected) + " abgewählt"
		flash += " " + itoa(deselected) + " abgewählte Zeile(n) übersprungen."
	}
	if len(failed) > 0 {
		audit += ", " + itoa(len(failed)) + " nicht gebucht"
		const named = 5
		shown := failed
		more := ""
		if len(shown) > named {
			shown, more = shown[:named], ", …"
		}
		flash += " " + itoa(len(failed)) + " Zeile(n) nicht gebucht: " + strings.Join(shown, ", ") + more + "."
	}
	return audit, flash
}

// handleImportCommit re-parses the CSV parked by the preview and creates the
// selected importable rows. A row the store refuses for a business reason (an
// invoice festgeschrieben meanwhile, an erased neighbor) is skipped with its
// reason and the loop goes on; whatever was created is audited even when an
// infrastructure error ends the commit early.
func (s *Server) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := formInt64(r, "year_id")
	back := "/entries/import?year=" + itoa64(yearID)
	// One-shot import: the token names the parked CSV, and keying each row
	// <token>:<line> makes a re-submitted commit (double-click, browser retry) a
	// no-op via CreateEntry's ON CONFLICT, without deduping two genuinely
	// identical rows in the same file (distinct lines).
	token := trimmed(r, "import_token")
	if s.tooLong(w, r, "Import-Token", token, maxNameLen) {
		redirect(w, r, back)
		return
	}
	if token == "" {
		s.setFlash(w, r, "error", msgImportExpired)
		redirect(w, r, back)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	raw, err := s.store.LoadImportUpload(r.Context(), token, store.ImportUploadBooking, s.currentUserID(r), yearID)
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", msgImportExpired)
		redirect(w, r, back)
		return
	} else if err != nil {
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
		redirect(w, r, back)
		return
	}
	// Re-check locks at commit time (not just preview): an invoice may have been
	// festgeschrieben between preview and commit. Locked rows get an error and are
	// skipped by the !row.OK() guard below.
	s.markLockedRows(r.Context(), yearID, rows)
	// Line selection (Ausbaukarte 69): with checkboxes present, only the ticked
	// lines are imported. No checkbox at all means an older page or a client
	// without them — fall back to "every OK row", the previous behavior.
	selected := map[int]bool{}
	hasSelection := len(r.PostForm["line"]) > 0
	for _, v := range r.PostForm["line"] {
		if n, err := strconv.Atoi(v); err == nil {
			selected[n] = true
		}
	}

	created, deselected := 0, 0
	var failed []string // "Zeile N (reason)" for selected rows that were not booked
	for _, row := range rows {
		if hasSelection && !selected[row.Line] {
			if row.OK() {
				deselected++
			}
			continue
		}
		if !row.OK() {
			// A ticked row that stopped being importable since the preview
			// (typically: its invoice was festgeschrieben) is reported, not
			// silently dropped.
			if hasSelection {
				failed = append(failed, "Zeile "+itoa(row.Line)+" ("+row.Err+")")
			}
			continue
		}
		e := &models.Entry{
			NeighborID: row.NeighborID, BillingYearID: yearID, Date: row.Date,
			TaskLabel: row.Task, Note: row.Note, Unit: row.Unit,
			Quantity: row.Qty, UnitPrice: row.Price, Cost: row.Cost,
			IdempotencyKey: token + ":" + strconv.Itoa(row.Line),
		}
		if row.Unit == "h" { // keep the hour-booking convention so it counts as hours
			e.Hours = row.Qty
			e.HourlyRate = row.Price
		}
		newID, err := s.store.CreateEntry(r.Context(), e, nil)
		if err != nil {
			reason, business := importCommitSkipReason(err)
			if !business {
				// Rows before this one are committed: leave their § 132 trail
				// before the 500, or they would exist without an audit record.
				detail, _ := importCommitSummary(created, deselected, append(failed, "Zeile "+itoa(row.Line)+" (Abbruch)"))
				s.audit(r, "import", "year", yearID, detail)
				s.serverError(w, r.URL.Path, err)
				return
			}
			failed = append(failed, "Zeile "+itoa(row.Line)+" ("+reason+")")
			continue
		}
		if newID != 0 { // 0 = an already-imported row on a re-submit; don't double-count
			created++
		}
	}
	detail, msg := importCommitSummary(created, deselected, failed)
	s.audit(r, "import", "year", yearID, detail)
	kind := "success"
	if len(failed) > 0 {
		kind = "error"
	}
	s.setFlash(w, r, kind, msg)
	redirect(w, r, dashboardURL(yearID))
}
