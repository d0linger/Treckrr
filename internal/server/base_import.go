package server

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// A rate list (Bemessungsgrundlage) arrives every few years and used to be typed
// in by hand. The CSV round trip below exports a basis, lets the operator edit
// it in a spreadsheet, and imports it as a NEW basis after a dry-run diff.
// Existing bases, years and bookings are never changed by an import.

const (
	maxRateListBytes = 256 << 10
	maxRateListRows  = 2000
)

// rateListHeader is the export header; the importer maps columns by these names.
var rateListHeader = []string{"Art", "Kennung", "Name", "PS", "Kosten je PS·h", "Arbeitsbreite (m)",
	"Kosten je m·h", "Kategorie", "Selbstkosten je h", "Aktiv"}

// rateColumn names a logical column of the rate-list format.
type rateColumn int

const (
	colKind rateColumn = iota
	colIdent
	colName
	colPS
	colCostPerPS
	colWidth
	colCostPerAB
	colCategory
	colSelfCost
	colActive
)

// rateHeaderNames maps normalized header spellings to logical columns.
var rateHeaderNames = map[string]rateColumn{
	"art": colKind, "typ": colKind, "kennung": colIdent, "name": colName, "bezeichnung": colName, "ps": colPS,
	"kosten je ps h": colCostPerPS, "kosten je ps": colCostPerPS,
	"arbeitsbreite m": colWidth, "arbeitsbreite": colWidth,
	"kosten je m h": colCostPerAB, "kosten je ab h": colCostPerAB, "kosten je ab": colCostPerAB,
	"kategorie": colCategory, "selbstkosten je h": colSelfCost, "selbstkosten": colSelfCost, "aktiv": colActive,
}

// normalizeRateHeader lowercases a header cell and strips units and punctuation.
func normalizeRateHeader(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "\uFEFF"))
	s = strings.NewReplacer("·", " ", "(", " ", ")", " ", "/", " ", "€", " ", ".", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// rateListRow is one parsed line with its validation result and, after the
// diff, its status against the comparison basis.
type rateListRow struct {
	Line      int
	Kind      string // load, tractor, machine
	Ident     string
	Name      string
	Category  string
	PS        decimal.Decimal
	CostPerPS decimal.Decimal
	Width     decimal.Decimal
	CostPerAB decimal.Decimal
	SelfCost  *decimal.Decimal
	Active    bool
	Err       string
	Status    string // neu, geändert, gleich
	Changes   []string
}

// KindLabel is the German item type shown in the preview.
func (r rateListRow) KindLabel() string { return rateKindLabel(r.Kind) }

// Key is the item's identity within a basis.
func (r rateListRow) Key() string {
	if r.Kind == "tractor" {
		return r.Ident
	}
	return r.Name
}

func rateKindLabel(kind string) string {
	switch kind {
	case "load":
		return "Belastungsstufe"
	case "tractor":
		return "Traktor"
	case "machine":
		return "Maschine"
	}
	return "Unbekannt"
}

// parseRateKind maps the Art cell to an item kind.
func parseRateKind(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "belastungsstufe", "belastung":
		return "load"
	case "traktor", "zugmaschine":
		return "tractor"
	case "maschine", "gerät", "anbaugerät":
		return "machine"
	}
	return ""
}

// parseRateActive reads the optional Aktiv cell; empty means active.
func parseRateActive(s string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "ja", "j", "1", "true", "x":
		return true, true
	case "nein", "n", "0", "false":
		return false, true
	}
	return false, false
}

// parseRateList reads a semicolon CSV with a header row. It returns a
// file-level message when the file cannot be used at all; row problems are
// reported per row so the preview can show every one of them.
func parseRateList(raw []byte) ([]rateListRow, string) {
	if len(raw) > maxRateListBytes {
		return nil, "Die Datei ist zu groß (höchstens 256 KB)."
	}
	cr := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))))
	cr.Comma = ';'
	cr.FieldsPerRecord = -1
	var columns map[rateColumn]int
	var rows []rateListRow
	seen := map[string]int{}
	line := 0
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "Die CSV-Datei konnte nicht gelesen werden (Zeile " + strconv.Itoa(line+1) + ")."
		}
		line++
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		if columns == nil {
			columns = map[rateColumn]int{}
			for i, cell := range rec {
				if c, ok := rateHeaderNames[normalizeRateHeader(cell)]; ok {
					if _, dup := columns[c]; !dup {
						columns[c] = i
					}
				}
			}
			_, hasKind := columns[colKind]
			_, hasName := columns[colName]
			if !hasKind || !hasName {
				return nil, "Die erste Zeile muss die Spaltenköpfe enthalten, mindestens „Art“ und „Name“. Am einfachsten eine bestehende Grundlage als CSV exportieren und diese Datei bearbeiten."
			}
			continue
		}
		if len(rows) >= maxRateListRows {
			return nil, "Die Datei hat zu viele Zeilen (höchstens " + strconv.Itoa(maxRateListRows) + ")."
		}
		cell := func(c rateColumn) string {
			if i, ok := columns[c]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		row := rateListRow{Line: line, Kind: parseRateKind(cell(colKind)), Ident: cell(colIdent), Name: cell(colName), Category: cell(colCategory)}
		row.Err = validateRateRow(&row, cell)
		if row.Err == "" {
			key := row.Kind + "\x00" + strings.ToLower(row.Key())
			if first, dup := seen[key]; dup {
				row.Err = "Doppelt, bereits in Zeile " + strconv.Itoa(first)
			} else {
				seen[key] = line
			}
		}
		rows = append(rows, row)
	}
	if columns == nil || len(rows) == 0 {
		return nil, "Die Datei enthält keine Einträge."
	}
	return rows, ""
}

// validateRateRow applies the same rules as the manual catalog forms.
func validateRateRow(row *rateListRow, cell func(rateColumn) string) string {
	decimalCell := func(c rateColumn) (decimal.Decimal, bool) { return parseGermanDecimalOK(cell(c)) }
	active, ok := parseRateActive(cell(colActive))
	if !ok {
		return "Aktiv muss „ja“ oder „nein“ sein"
	}
	row.Active = active
	for _, field := range []struct{ label, value string }{{"Kennung", row.Ident}, {"Name", row.Name}, {"Kategorie", row.Category}} {
		if msg := lenError(field.label, field.value, maxNameLen); msg != "" {
			return msg
		}
	}
	switch row.Kind {
	case "load":
		if row.Name == "" {
			return "Name der Belastungsstufe fehlt"
		}
		cost, ok := decimalCell(colCostPerPS)
		if !ok || cost.IsNegative() {
			return "Kosten je PS·h fehlen oder sind ungültig"
		}
		row.CostPerPS = cost
	case "tractor":
		if row.Ident == "" {
			return "Kennung des Traktors fehlt"
		}
		ps, ok := decimalCell(colPS)
		if !ok || !ps.IsPositive() {
			return "PS fehlen oder sind nicht größer als 0"
		}
		row.PS = ps
	case "machine":
		if row.Name == "" {
			return "Name der Maschine fehlt"
		}
		width, ok := decimalCell(colWidth)
		if !ok || !width.IsPositive() {
			return "Arbeitsbreite fehlt oder ist nicht größer als 0"
		}
		cost, ok := decimalCell(colCostPerAB)
		if !ok || cost.IsNegative() {
			return "Kosten je m·h fehlen oder sind ungültig"
		}
		row.Width, row.CostPerAB = width, cost
		if raw := cell(colSelfCost); raw != "" {
			selfCost, ok := parseGermanDecimalOK(raw)
			if !ok || selfCost.IsNegative() {
				return "Selbstkosten je h sind ungültig"
			}
			row.SelfCost = &selfCost
		}
	default:
		return "Unbekannte Art (Belastungsstufe, Traktor oder Maschine)"
	}
	return ""
}

// rateNum renders a rate with a decimal comma and without trailing zeros.
func rateNum(d decimal.Decimal) string { return strings.Replace(d.String(), ".", ",", 1) }

// rateChange describes one changed value, with the relative change for rates.
func rateChange(label string, old, updated decimal.Decimal) string {
	text := label + " " + rateNum(old) + " → " + rateNum(updated)
	if old.IsPositive() {
		pct := updated.Sub(old).Div(old).Mul(decimal.NewFromInt(100)).Round(1)
		sign := ""
		if pct.IsPositive() {
			sign = "+"
		}
		text += " (" + sign + strings.Replace(pct.StringFixed(1), ".", ",", 1) + " %)"
	}
	return text
}

// rateRemoved is an item of the comparison basis that the file no longer has.
type rateRemoved struct {
	KindLabel string
	Key       string
}

// rateListPreview is the dry run shown before a basis is created.
type rateListPreview struct {
	Rows                       []rateListRow
	Removed                    []rateRemoved
	New, Changed, Same, Errors int
	SourceName                 string
	Valid                      bool
}

// diffRateList marks every valid row as new, changed or unchanged against the
// comparison basis and lists its items that the file drops.
func diffRateList(rows []rateListRow, loads []models.LoadLevel, tractors []models.Tractor, machines []models.Machine) rateListPreview {
	p := rateListPreview{Rows: rows}
	inFile := map[string]bool{}
	loadBy, tractorBy, machineBy := map[string]models.LoadLevel{}, map[string]models.Tractor{}, map[string]models.Machine{}
	for _, l := range loads {
		loadBy[strings.ToLower(l.Name)] = l
	}
	for _, t := range tractors {
		tractorBy[strings.ToLower(t.Ident)] = t
	}
	for _, m := range machines {
		machineBy[strings.ToLower(m.Name)] = m
	}
	for i := range p.Rows {
		row := &p.Rows[i]
		if row.Err != "" {
			p.Errors++
			continue
		}
		key := strings.ToLower(row.Key())
		inFile[row.Kind+"\x00"+key] = true
		var changes []string
		found := false
		switch row.Kind {
		case "load":
			if old, ok := loadBy[key]; ok {
				found = true
				if !old.CostPerPS.Equal(row.CostPerPS) {
					changes = append(changes, rateChange("Kosten je PS·h", old.CostPerPS, row.CostPerPS))
				}
			}
		case "tractor":
			if old, ok := tractorBy[key]; ok {
				found = true
				if old.Name != row.Name {
					changes = append(changes, "Name „"+old.Name+"“ → „"+row.Name+"“")
				}
				if !old.PS.Equal(row.PS) {
					changes = append(changes, rateChange("PS", old.PS, row.PS))
				}
				if old.Active != row.Active {
					changes = append(changes, activeChange(row.Active))
				}
			}
		case "machine":
			if old, ok := machineBy[key]; ok {
				found = true
				if !old.WorkingWidth.Equal(row.Width) {
					changes = append(changes, rateChange("Arbeitsbreite", old.WorkingWidth, row.Width))
				}
				if !old.CostPerAB.Equal(row.CostPerAB) {
					changes = append(changes, rateChange("Kosten je m·h", old.CostPerAB, row.CostPerAB))
				}
				if oldRate, newRate := old.WorkingWidth.Mul(old.CostPerAB), row.Width.Mul(row.CostPerAB); !oldRate.Equal(newRate) {
					changes = append(changes, rateChange("Maschinensatz €/h", oldRate.Round(2), newRate.Round(2)))
				}
				if old.Category != row.Category {
					changes = append(changes, "Kategorie „"+old.Category+"“ → „"+row.Category+"“")
				}
				if row.SelfCost != nil && !old.SelfCostPerH.Equal(*row.SelfCost) {
					changes = append(changes, rateChange("Selbstkosten je h", old.SelfCostPerH, *row.SelfCost))
				}
				if old.Active != row.Active {
					changes = append(changes, activeChange(row.Active))
				}
			}
		}
		row.Changes = changes
		switch {
		case !found:
			row.Status = "neu"
			p.New++
		case len(changes) > 0:
			row.Status = "geändert"
			p.Changed++
		default:
			row.Status = "gleich"
			p.Same++
		}
	}
	for _, l := range loads {
		if !inFile["load\x00"+strings.ToLower(l.Name)] {
			p.Removed = append(p.Removed, rateRemoved{rateKindLabel("load"), l.Name})
		}
	}
	for _, t := range tractors {
		if !inFile["tractor\x00"+strings.ToLower(t.Ident)] {
			p.Removed = append(p.Removed, rateRemoved{rateKindLabel("tractor"), t.Ident})
		}
	}
	for _, m := range machines {
		if !inFile["machine\x00"+strings.ToLower(m.Name)] {
			p.Removed = append(p.Removed, rateRemoved{rateKindLabel("machine"), m.Name})
		}
	}
	p.Valid = p.Errors == 0
	return p
}

func activeChange(active bool) string {
	if active {
		return "wieder aktiv"
	}
	return "inaktiv"
}

// rateList converts validated rows into the store's import shape.
func rateList(rows []rateListRow) store.RateList {
	var list store.RateList
	for _, row := range rows {
		switch row.Kind {
		case "load":
			list.Loads = append(list.Loads, store.RateLoad{Name: row.Name, CostPerPS: row.CostPerPS})
		case "tractor":
			list.Tractors = append(list.Tractors, store.RateTractor{Ident: row.Ident, Name: row.Name, PS: row.PS, Active: row.Active})
		case "machine":
			list.Machines = append(list.Machines, store.RateMachine{Name: row.Name, Category: row.Category,
				Width: row.Width, CostPerAB: row.CostPerAB, SelfCostPerH: row.SelfCost, Active: row.Active})
		}
	}
	return list
}

// handleBaseExport downloads a basis in the rate-list format, the template for
// the next import.
func (s *Server) handleBaseExport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	base, err := s.store.GetBase(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	loads, err := s.store.ListLoadLevels(r.Context(), id)
	if err != nil {
		s.serverError(w, "base export: loads", err)
		return
	}
	tractors, err := s.store.ListTractors(r.Context(), id)
	if err != nil {
		s.serverError(w, "base export: tractors", err)
		return
	}
	machines, err := s.store.ListMachines(r.Context(), id)
	if err != nil {
		s.serverError(w, "base export: machines", err)
		return
	}
	data, err := rateListCSV(loads, tractors, machines, true)
	if err != nil {
		s.serverError(w, "base export: csv", err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="Bemessungsgrundlage_`+strconv.Itoa(base.Year)+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// rateListCSV writes a basis in the rate-list format. spreadsheet=true guards
// text cells against formula injection for files opened in a spreadsheet; the
// internal uplift round trip passes false so names survive unchanged.
func rateListCSV(loads []models.LoadLevel, tractors []models.Tractor, machines []models.Machine, spreadsheet bool) ([]byte, error) {
	text := func(s string) string {
		if spreadsheet {
			return csvSafe(s)
		}
		return s
	}
	yesNo := func(b bool) string {
		if b {
			return "ja"
		}
		return "nein"
	}
	var buf bytes.Buffer
	buf.WriteString("\uFEFF")
	cw := csv.NewWriter(&buf)
	cw.Comma = ';'
	_ = cw.Write(rateListHeader)
	for _, l := range loads {
		_ = cw.Write([]string{"Belastungsstufe", "", text(l.Name), "", rateNum(l.CostPerPS), "", "", "", "", "ja"})
	}
	for _, t := range tractors {
		_ = cw.Write([]string{"Traktor", text(t.Ident), text(t.Name), rateNum(t.PS), "", "", "", "", "", yesNo(t.Active)})
	}
	for _, m := range machines {
		_ = cw.Write([]string{"Maschine", "", text(m.Name), "", "", rateNum(m.WorkingWidth), rateNum(m.CostPerAB),
			text(m.Category), rateNum(m.SelfCostPerH), yesNo(m.Active)})
	}
	cw.Flush()
	return buf.Bytes(), cw.Error()
}

// upliftSteps are the offered roundings for adjusted rates, in euros.
var upliftSteps = []string{"0.01", "0.0001", "0.05", "0.1"}

// upliftRate raises a rate by pct percent and rounds to the step.
func upliftRate(rate, pct, step decimal.Decimal) decimal.Decimal {
	raised := rate.Mul(decimal.NewFromInt(100).Add(pct)).Div(decimal.NewFromInt(100))
	return raised.Div(step).Round(0).Mul(step)
}

// handleBaseUpliftPreview builds a new basis from an existing one with load
// level and machine rates changed by a percentage, and shows it in the same
// dry run as an imported rate list. Committing goes through the import path,
// so the new basis, the rig copy and the audit entry work identically.
func (s *Server) handleBaseUpliftPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	req := rateImportRequest{Year: formInt(r, "year"), Name: trimmed(r, "name"), SourceID: formInt64(r, "source_base_id")}
	if req.SourceID <= 0 {
		s.setFlash(w, r, "error", "Bitte die Grundlage wählen, deren Sätze angepasst werden.")
		redirect(w, r, "/bases#anpassen")
		return
	}
	if req.Year < 1900 || req.Year > 3000 {
		s.setFlash(w, r, "error", "Bitte ein gültiges „gültig ab“-Jahr angeben.")
		redirect(w, r, "/bases#anpassen")
		return
	}
	if msg := lenError("Name", req.Name, maxNameLen); msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, "/bases#anpassen")
		return
	}
	if req.Name == "" {
		req.Name = "Bemessungsgrundlage " + strconv.Itoa(req.Year)
	}
	limit := func(field string) (decimal.Decimal, bool) {
		raw := strings.TrimSpace(r.FormValue(field))
		if raw == "" {
			return decimal.Zero, true
		}
		d, ok := parseGermanDecimalOK(raw)
		return d, ok && d.GreaterThanOrEqual(decimal.NewFromInt(-50)) && d.LessThanOrEqual(decimal.NewFromInt(100))
	}
	pctLoad, okLoad := limit("pct_load")
	pctMachine, okMachine := limit("pct_machine")
	if !okLoad || !okMachine {
		s.setFlash(w, r, "error", "Die Anpassung muss zwischen -50 und +100 Prozent liegen.")
		redirect(w, r, "/bases#anpassen")
		return
	}
	stepRaw := r.FormValue("round")
	step := decimal.RequireFromString("0.01")
	for _, offered := range upliftSteps {
		if stepRaw == offered {
			step = decimal.RequireFromString(offered)
		}
	}
	source, err := s.store.GetBase(r.Context(), req.SourceID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	loads, err := s.store.ListLoadLevels(r.Context(), source.ID)
	if err != nil {
		s.serverError(w, "base uplift: loads", err)
		return
	}
	tractors, err := s.store.ListTractors(r.Context(), source.ID)
	if err != nil {
		s.serverError(w, "base uplift: tractors", err)
		return
	}
	machines, err := s.store.ListMachines(r.Context(), source.ID)
	if err != nil {
		s.serverError(w, "base uplift: machines", err)
		return
	}
	for i := range loads {
		loads[i].CostPerPS = upliftRate(loads[i].CostPerPS, pctLoad, step)
	}
	for i := range machines {
		machines[i].CostPerAB = upliftRate(machines[i].CostPerAB, pctMachine, step)
	}
	if req.Raw, err = rateListCSV(loads, tractors, machines, false); err != nil {
		s.serverError(w, "base uplift: csv", err)
		return
	}
	preview, fileErr, err := s.rateListPreviewFor(r, req)
	if err != nil {
		s.serverError(w, "base uplift: preview", err)
		return
	}
	if fileErr != "" {
		s.setFlash(w, r, "error", fileErr)
		redirect(w, r, "/bases#anpassen")
		return
	}
	s.renderRateImport(w, r, req, preview)
}

// rateImportRequest is the form shared by preview and commit.
type rateImportRequest struct {
	Year     int
	Name     string
	SourceID int64
	Raw      []byte
}

// readRateImportRequest reads year, name, comparison basis and the CSV from an
// upload (preview) or the hidden text field (commit). ok=false means a flash
// was set and the caller should return to /bases.
func (s *Server) readRateImportRequest(w http.ResponseWriter, r *http.Request) (rateImportRequest, bool) {
	if err := r.ParseMultipartForm(maxRateListBytes + 64<<10); err != nil && !errors.Is(err, http.ErrNotMultipart) {
		s.setFlash(w, r, "error", "Die Datei konnte nicht gelesen werden (höchstens 256 KB).")
		return rateImportRequest{}, false
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	req := rateImportRequest{Year: formInt(r, "year"), Name: trimmed(r, "name"), SourceID: formInt64(r, "source_base_id")}
	if req.Year < 1900 || req.Year > 3000 {
		s.setFlash(w, r, "error", "Bitte ein gültiges „gültig ab“-Jahr angeben.")
		return req, false
	}
	if msg := lenError("Name", req.Name, maxNameLen); msg != "" {
		s.setFlash(w, r, "error", msg)
		return req, false
	}
	if req.Name == "" {
		req.Name = "Bemessungsgrundlage " + strconv.Itoa(req.Year)
	}
	if text := r.FormValue("csv"); text != "" {
		req.Raw = []byte(text)
	} else {
		file, _, err := r.FormFile("file")
		if err != nil {
			s.setFlash(w, r, "error", "Bitte eine CSV-Datei wählen.")
			return req, false
		}
		defer file.Close()
		req.Raw, err = io.ReadAll(io.LimitReader(file, maxRateListBytes+1))
		if err != nil {
			s.setFlash(w, r, "error", "Die Datei konnte nicht gelesen werden.")
			return req, false
		}
	}
	return req, true
}

// rateListPreviewFor parses the request and diffs it against its comparison
// basis. fileErr is a file-level message that makes the import impossible.
func (s *Server) rateListPreviewFor(r *http.Request, req rateImportRequest) (rateListPreview, string, error) {
	rows, fileErr := parseRateList(req.Raw)
	if fileErr != "" {
		return rateListPreview{}, fileErr, nil
	}
	var (
		loads    []models.LoadLevel
		tractors []models.Tractor
		machines []models.Machine
		source   string
	)
	if req.SourceID > 0 {
		base, err := s.store.GetBase(r.Context(), req.SourceID)
		if err != nil {
			return rateListPreview{}, "Die Vergleichsgrundlage existiert nicht mehr.", nil
		}
		source = strconv.Itoa(base.Year) + " — " + base.Name
		if loads, err = s.store.ListLoadLevels(r.Context(), req.SourceID); err != nil {
			return rateListPreview{}, "", err
		}
		if tractors, err = s.store.ListTractors(r.Context(), req.SourceID); err != nil {
			return rateListPreview{}, "", err
		}
		if machines, err = s.store.ListMachines(r.Context(), req.SourceID); err != nil {
			return rateListPreview{}, "", err
		}
	}
	preview := diffRateList(rows, loads, tractors, machines)
	preview.SourceName = source
	return preview, "", nil
}

// handleBaseImportPreview shows the dry run of a rate-list import.
func (s *Server) handleBaseImportPreview(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRateImportRequest(w, r)
	if !ok {
		redirect(w, r, "/bases")
		return
	}
	preview, fileErr, err := s.rateListPreviewFor(r, req)
	if err != nil {
		s.serverError(w, "base import preview", err)
		return
	}
	if fileErr != "" {
		s.setFlash(w, r, "error", fileErr)
		redirect(w, r, "/bases")
		return
	}
	s.renderRateImport(w, r, req, preview)
}

func (s *Server) renderRateImport(w http.ResponseWriter, r *http.Request, req rateImportRequest, preview rateListPreview) {
	data := s.newPage(w, r, "Preisliste prüfen", "bases")
	data["Preview"] = preview
	data["ImportYear"] = req.Year
	data["ImportName"] = req.Name
	data["ImportSourceID"] = req.SourceID
	data["ImportCSV"] = string(req.Raw)
	s.render(w, r, "base_import", data)
}

// handleBaseImportCommit creates the new basis after re-validating the exact
// text the preview showed.
func (s *Server) handleBaseImportCommit(w http.ResponseWriter, r *http.Request) {
	req, ok := s.readRateImportRequest(w, r)
	if !ok {
		redirect(w, r, "/bases")
		return
	}
	preview, fileErr, err := s.rateListPreviewFor(r, req)
	if err != nil {
		s.serverError(w, "base import", err)
		return
	}
	if fileErr != "" {
		s.setFlash(w, r, "error", fileErr)
		redirect(w, r, "/bases")
		return
	}
	if !preview.Valid {
		s.renderRateImport(w, r, req, preview)
		return
	}
	result, err := s.store.CreateBaseFromRateList(r.Context(), req.Year, req.Name, rateList(preview.Rows), req.SourceID)
	if err != nil {
		s.serverError(w, "base import: create", err)
		return
	}
	detail := req.Name + " · aus Preisliste: " + strconv.Itoa(preview.New) + " neu, " + strconv.Itoa(preview.Changed) +
		" geändert, " + strconv.Itoa(preview.Same) + " unverändert, " + strconv.Itoa(len(preview.Removed)) + " entfallen"
	s.audit(r, "import", "base", result.BaseID, detail)
	msg := "Bemessungsgrundlage " + strconv.Itoa(req.Year) + " aus der Preisliste angelegt."
	if result.Gespanne > 0 {
		msg += " " + strconv.Itoa(result.Gespanne) + " Gespann(e) übernommen."
	}
	if len(result.GespanneSkipped) > 0 {
		msg += " Nicht übernommen, weil Teile fehlen: " + strings.Join(result.GespanneSkipped, ", ") + "."
	}
	s.setFlash(w, r, "success", msg)
	redirect(w, r, "/prices?base="+itoa64(result.BaseID))
}
