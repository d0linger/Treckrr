package server

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/einvoice"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// ---- Rechnungsjournal (Ausbaukarte 47/50/52/55) ----------------------------

// uvaPeriod is one month's Bemessungsgrundlage + USt (the U30 basis).
type uvaPeriod struct {
	Label string // MM/JJJJ
	Net   decimal.Decimal
	VAT   decimal.Decimal
	Gross decimal.Decimal
}

// uvaRate is the same sums grouped by VAT rate — several rates aggregate
// cleanly here even though a single invoice still carries one rate.
type uvaRate struct {
	Rate decimal.Decimal
	Net  decimal.Decimal
	VAT  decimal.Decimal
}

// journalTotals sums the signed revenue rows (JournalRow.CountsForRevenue).
func journalTotals(rows []store.JournalRow) (net, vat, gross decimal.Decimal) {
	for _, j := range rows {
		if !j.CountsForRevenue() {
			continue
		}
		net = net.Add(j.Net)
		vat = vat.Add(j.VATAmount)
		gross = gross.Add(j.Gross)
	}
	return
}

// uvaBreakdown groups the signed revenue by issue month and by VAT rate.
func uvaBreakdown(rows []store.JournalRow) ([]uvaPeriod, []uvaRate) {
	months := map[string]*uvaPeriod{}
	rates := map[string]*uvaRate{}
	for _, j := range rows {
		if !j.CountsForRevenue() {
			continue
		}
		mk := j.IssuedOn.Format("01/2006")
		m, ok := months[mk]
		if !ok {
			m = &uvaPeriod{Label: mk}
			months[mk] = m
		}
		m.Net = m.Net.Add(j.Net)
		m.VAT = m.VAT.Add(j.VATAmount)
		m.Gross = m.Gross.Add(j.Gross)
		rk := j.VATRate.StringFixed(1)
		rt, ok := rates[rk]
		if !ok {
			rt = &uvaRate{Rate: j.VATRate}
			rates[rk] = rt
		}
		rt.Net = rt.Net.Add(j.Net)
		rt.VAT = rt.VAT.Add(j.VATAmount)
	}
	periods := make([]uvaPeriod, 0, len(months))
	for _, m := range months {
		periods = append(periods, *m)
	}
	sort.Slice(periods, func(i, k int) bool { // MM/JJJJ → sort by year, then month
		return periods[i].Label[3:]+periods[i].Label[:2] < periods[k].Label[3:]+periods[k].Label[:2]
	})
	rateRows := make([]uvaRate, 0, len(rates))
	for _, rt := range rates {
		rateRows = append(rateRows, *rt)
	}
	sort.Slice(rateRows, func(i, k int) bool { return rateRows[i].Rate.LessThan(rateRows[k].Rate) })
	return periods, rateRows
}

// handleJournal renders the per-year Rechnungsjournal: every issued document
// with Netto/USt/Brutto, the signed totals, and the monthly UVA basis.
func (s *Server) handleJournal(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ListInvoiceJournal(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	net, vat, gross := journalTotals(rows)
	periods, rates := uvaBreakdown(rows)
	data := s.newPage(w, r, "Rechnungsjournal", "journal")
	data["Year"] = year
	data["Rows"] = rows
	data["TotalNet"], data["TotalVAT"], data["TotalGross"] = net, vat, gross
	data["UVAPeriods"] = periods
	data["UVARates"] = rates
	data["ShowUVA"] = company.TaxMode == "regel"
	profiles, err := s.store.ListAccountingExportProfiles(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["AccountingProfiles"] = profiles
	data["AccountingColumns"] = accountingColumns
	// Kleinunternehmer ceiling (Nr. 55): only when configured (>0) and relevant.
	if company.TaxMode == "kleinunternehmer" && company.SmallBusinessLimit.IsPositive() {
		kuSum, err := s.store.KUCalendarYearGross(r.Context(), year.Year)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		pct := kuSum.Div(company.SmallBusinessLimit).Mul(decimal.NewFromInt(100)).Round(0)
		data["KUSum"] = kuSum
		data["KULimit"] = company.SmallBusinessLimit
		data["KUPct"] = pct
		data["KUNear"] = pct.GreaterThanOrEqual(decimal.NewFromInt(80))
	}
	s.render(w, r, "rechnungsjournal", data)
}

var accountingColumns = []struct {
	Key   string
	Label string
}{
	{"number", "Belegnummer"}, {"date", "Datum"}, {"kind", "Belegart"},
	{"neighbor", "Nachbar"}, {"net", "Netto"}, {"vat_rate", "USt-Satz"},
	{"vat", "USt"}, {"gross", "Brutto"}, {"revenue_account", "Erlöskonto"},
	{"receivable_account", "Debitorenkonto"}, {"tax_code", "Steuercode"},
	{"cost_center", "Kostenstelle"},
}

var accountingColumnLabels = func() map[string]string {
	m := make(map[string]string, len(accountingColumns))
	for _, col := range accountingColumns {
		m[col.Key] = col.Label
	}
	return m
}()

func validAccountingColumns(raw string) ([]string, bool) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		key := strings.TrimSpace(part)
		if _, ok := accountingColumnLabels[key]; !ok || seen[key] {
			return nil, false
		}
		seen[key] = true
		out = append(out, key)
	}
	return out, len(out) > 0
}

func (s *Server) handleAccountingProfileSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	back := "/rechnungsjournal"
	if yearID := s.yearIDFromForm(r); yearID != 0 {
		back += "?year=" + strconv.FormatInt(yearID, 10)
	}
	name := trimmed(r, "name")
	columns := strings.Join(r.PostForm["columns"], ",")
	if name == "" || s.tooLong(w, r, "Profilname", name, maxNameLen) {
		if name == "" {
			s.setFlash(w, r, "error", "Ein Profilname ist erforderlich.")
		}
		redirect(w, r, back)
		return
	}
	if _, ok := validAccountingColumns(columns); !ok {
		s.setFlash(w, r, "error", "Bitte mindestens eine gültige Exportspalte wählen.")
		redirect(w, r, back)
		return
	}
	delimiter := r.FormValue("delimiter")
	if delimiter != "," {
		delimiter = ";"
	}
	p := store.AccountingExportProfile{
		ID: formInt64(r, "id"), Name: name, RevenueAccount: trimmed(r, "revenue_account"),
		ReceivableAccount: trimmed(r, "receivable_account"), TaxCode: trimmed(r, "tax_code"),
		CostCenter: trimmed(r, "cost_center"), Columns: columns, Delimiter: delimiter,
		DecimalComma: r.FormValue("decimal_comma") == "true",
	}
	for label, value := range map[string]string{
		"Erlöskonto": p.RevenueAccount, "Debitorenkonto": p.ReceivableAccount,
		"Steuercode": p.TaxCode, "Kostenstelle": p.CostCenter,
	} {
		if s.tooLong(w, r, label, value, maxNameLen) {
			redirect(w, r, back)
			return
		}
	}
	id, err := s.store.SaveAccountingExportProfile(r.Context(), p)
	if err != nil {
		s.setFlash(w, r, "error", "Exportprofil konnte nicht gespeichert werden (Name bereits vergeben?).")
	} else {
		action := "create"
		if p.ID != 0 {
			action = "update"
		}
		s.audit(r, action, "accounting_export_profile", id, name)
		s.setFlash(w, r, "success", "Buchhaltungsprofil gespeichert.")
	}
	redirect(w, r, back)
}

func (s *Server) handleAccountingProfileDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := "/rechnungsjournal"
	if yearID := s.yearIDFromForm(r); yearID != 0 {
		back += "?year=" + strconv.FormatInt(yearID, 10)
	}
	err = s.store.DeleteAccountingExportProfile(r.Context(), id)
	if err != nil {
		s.setFlash(w, r, "error", "Exportprofil konnte nicht gelöscht werden.")
	} else {
		s.audit(r, "delete", "accounting_export_profile", id, "")
		s.setFlash(w, r, "success", "Buchhaltungsprofil gelöscht.")
	}
	redirect(w, r, back)
}

func accountingCell(row store.JournalRow, profile store.AccountingExportProfile, key string) string {
	switch key {
	case "number":
		return csvSafe(row.Number)
	case "date":
		return row.IssuedOn.Format("2006-01-02")
	case "kind":
		return row.Kind
	case "neighbor":
		return csvSafe(row.NeighborName)
	case "net", "vat_rate", "vat", "gross":
		value := map[string]decimal.Decimal{
			"net": row.Net, "vat_rate": row.VATRate, "vat": row.VATAmount, "gross": row.Gross,
		}[key].StringFixed(2)
		if profile.DecimalComma {
			value = strings.ReplaceAll(value, ".", ",")
		}
		return value
	case "revenue_account":
		return csvSafe(profile.RevenueAccount)
	case "receivable_account":
		return csvSafe(profile.ReceivableAccount)
	case "tax_code":
		return csvSafe(profile.TaxCode)
	case "cost_center":
		return csvSafe(profile.CostCenter)
	default:
		return ""
	}
}

// handleAccountingCSV exports immutable invoice journal values through one
// explicit mapping profile. It does not modify or infer accounting entries.
func (s *Server) handleAccountingCSV(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	profileID, err := strconv.ParseInt(r.URL.Query().Get("profile"), 10, 64)
	if err != nil || profileID <= 0 {
		s.badRequest(w, "Bitte ein gültiges Buchhaltungsprofil wählen.")
		return
	}
	profile, err := s.store.GetAccountingExportProfile(r.Context(), profileID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	columns, ok := validAccountingColumns(profile.Columns)
	if !ok {
		s.serverError(w, r.URL.Path, errors.New("invalid accounting export profile columns"))
		return
	}
	rows, err := s.store.ListInvoiceJournal(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	filename := fmt.Sprintf("buchhaltung-%d-%d.csv", year.Year, profile.ID)
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	if profile.Delimiter == "," {
		cw.Comma = ','
	} else {
		cw.Comma = ';'
	}
	header := make([]string, len(columns))
	for i, key := range columns {
		header[i] = accountingColumnLabels[key]
	}
	_ = cw.Write(header)
	for _, row := range rows {
		if !row.CountsForRevenue() {
			continue
		}
		line := make([]string, len(columns))
		for i, key := range columns {
			line[i] = accountingCell(row, profile, key)
		}
		_ = cw.Write(line)
	}
	cw.Flush()
}

// writeJournalCSV writes the journal in the export CSV dialect (BOM + ';').
func writeJournalCSV(w *csv.Writer, rows []store.JournalRow) {
	_ = w.Write([]string{"Nummer", "Datum", "Art", "Status", "Nachbar", "Netto (€)", "USt-Satz (%)", "USt (€)", "Brutto (€)"})
	kinds := map[string]string{"invoice": "Rechnung", "storno": "Storno", "gutschrift": "Gutschrift", "anzahlung": "Abschlag"}
	status := map[string]string{"issued": "ausgestellt", "canceled": "storniert"}
	for _, j := range rows {
		// csvSafe on the free-text columns, like every other export: a neighbor
		// name or invoice number starting with = + - @ would otherwise be
		// evaluated as a formula by the spreadsheet this file is opened in.
		// Amount columns stay raw — a quote would break the negative sign.
		_ = w.Write([]string{
			csvSafe(j.Number), j.IssuedOn.Format("02.01.2006"), orKey(kinds, j.Kind), orKey(status, j.Status), csvSafe(j.NeighborName),
			deDecimal(j.Net), deDecimal(j.VATRate), deDecimal(j.VATAmount), deDecimal(j.Gross),
		})
	}
	net, vat, gross := journalTotals(rows)
	_ = w.Write([]string{"", "", "", "", "Summe (signiert)", deDecimal(net), "", deDecimal(vat), deDecimal(gross)})
}

func orKey(m map[string]string, k string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return k
}

// handleJournalCSV exports the journal as CSV for the tax adviser.
func (s *Server) handleJournalCSV(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	rows, err := s.store.ListInvoiceJournal(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	filename := fmt.Sprintf("rechnungsjournal-%d.csv", year.Year)
	cw, finish := csvDownload(w, r, filename)
	defer finish()
	writeJournalCSV(cw, rows)
}

var zipNameSafe = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// handleJournalZip streams the Belegarchiv (Nr. 50): every document of the
// year as its frozen PDF plus the journal CSV, in one ZIP. A legacy row whose
// snapshot cannot be reconstructed is listed in a note file instead of being
// silently dropped. (ebInterface-XML deliberately not included — that format
// choice is the user's, see Ausbaukarte.)
func (s *Server) handleJournalZip(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	docs, err := s.store.ListInvoiceDocs(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	journal, err := s.store.ListInvoiceJournal(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Built fully in memory before the first byte is sent: PDF rendering can
	// fail, and a half-streamed ZIP behind HTTP 200 is a corrupt download. The
	// volume is bounded (a year's invoices).
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	var missing []string
	for i := range docs {
		iv := &docs[i]
		if iv.Content == nil {
			missing = append(missing, iv.Number)
			continue
		}
		b, err := pdf.RenderInvoice(iv)
		if err != nil {
			missing = append(missing, iv.Number)
			continue
		}
		name := zipNameSafe.ReplaceAllString(iv.Number, "_") + "_" + iv.Kind + ".pdf"
		f, err := zw.Create(name)
		if err != nil {
			s.serverError(w, "journal zip", err)
			return
		}
		_, _ = f.Write(b)
		if iv.Kind == "invoice" && iv.Status == "issued" {
			if xb, xerr := einvoice.Render(*iv); xerr == nil {
				if xf, createErr := zw.Create(zipNameSafe.ReplaceAllString(iv.Number, "_") + "_ebinterface-6p1.xml"); createErr == nil {
					_, _ = xf.Write(xb)
				}
			}
		}
	}
	var csvBuf bytes.Buffer
	csvBuf.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(&csvBuf)
	cw.Comma = ';'
	writeJournalCSV(cw, journal)
	cw.Flush()
	if f, err := zw.Create(fmt.Sprintf("rechnungsjournal-%d.csv", year.Year)); err == nil {
		_, _ = f.Write(csvBuf.Bytes())
	}
	if len(missing) > 0 {
		if f, err := zw.Create("HINWEIS-fehlende-snapshots.txt"); err == nil {
			_, _ = f.Write([]byte("Für folgende Dokumente liegt kein Snapshot vor (Alt-Daten vor der Festschreibung); sie fehlen als PDF:\r\n" +
				strings.Join(missing, "\r\n") + "\r\n"))
		}
	}
	if err := zw.Close(); err != nil {
		s.serverError(w, "journal zip", err)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"belegarchiv-%d.zip\"", year.Year))
	_, _ = w.Write(buf.Bytes())
}

// kuIssueNote returns the Kleinunternehmer warning to append to an issue flash,
// or "" — measured against the CALENDAR year of the new document's date.
func (s *Server) kuIssueNote(r *http.Request, company models.Company, calYear int) string {
	if company.TaxMode != "kleinunternehmer" || !company.SmallBusinessLimit.IsPositive() {
		return ""
	}
	sum, err := s.store.KUCalendarYearGross(r.Context(), calYear)
	if err != nil {
		return ""
	}
	if sum.GreaterThan(company.SmallBusinessLimit) {
		return fmt.Sprintf(" Achtung: Kleinunternehmergrenze überschritten (%s € von %s €, Kalenderjahr %d).",
			sum.StringFixed(2), company.SmallBusinessLimit.StringFixed(2), calYear)
	}
	if sum.GreaterThanOrEqual(company.SmallBusinessLimit.Mul(decimal.NewFromFloat(0.8))) {
		return fmt.Sprintf(" Hinweis: %s € von %s € der Kleinunternehmergrenze erreicht (Kalenderjahr %d).",
			sum.StringFixed(2), company.SmallBusinessLimit.StringFixed(2), calYear)
	}
	return ""
}

// ---- Freie Gutschrift und Anzahlung (Ausbaukarte 53/54) --------------------

// handleFreeGutschrift issues a standalone credit note — the correction path
// that used to require an active invoice.
func (s *Server) handleFreeGutschrift(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID)
	if !s.requireOpenYear(w, r, yearID, back) {
		return
	}
	note := trimmed(r, "note")
	if s.tooLong(w, r, "Grund", note, maxNoteLen) || s.tooLong(w, r, "Betrag", r.FormValue("amount"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.serverError(w, "free gutschrift: year", err)
		return
	}
	amount := formDecimal(r, "amount").Abs()
	if models.HasSubCent(amount) {
		s.setFlash(w, r, "error", msgMoneyCents)
		redirect(w, r, back)
		return
	}
	gv, err := s.store.FreeGutschrift(r.Context(), yearID, neighborID, year.Year, amount, note)
	switch {
	case errors.Is(err, store.ErrAmountRequired):
		s.setFlash(w, r, "error", "Bitte einen Betrag größer 0 eingeben.")
	case errors.Is(err, store.ErrGutschriftExceedsBookings):
		s.setFlash(w, r, "error", "Die Gutschrift übersteigt den Betrag, den die Rechnung derzeit hätte (erfasste Leistungen brutto abzüglich bereits erteilter Gutschriften). Ohne Rechnung ist keine höhere Gutschrift möglich.")
	case errors.Is(err, store.ErrGutschriftTooLarge):
		s.setFlash(w, r, "error", "Gutschrift übersteigt den noch nicht gutgeschriebenen Rechnungsbetrag.")
	case err != nil:
		s.setFlash(w, r, "error", "Gutschrift fehlgeschlagen.")
	default:
		s.setFlash(w, r, "success", "Gutschrift "+gv.Number+" erstellt.")
	}
	redirect(w, r, back)
}

// handleAnzahlungCreate issues an Abschlag for a partial payment during the
// season (Ausbaukarte 54).
func (s *Server) handleAnzahlungCreate(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID)
	if s.tooLong(
		w, r, "Fällig am", r.FormValue("due_on"), 50,
	) {
		redirect(w, r, back)
		return
	}
	if !s.requireOpenYear(w, r, yearID, back) {
		return
	}
	label := trimmed(r, "label")
	if s.tooLong(w, r, "Bezeichnung", label, maxNameLen) || s.tooLong(w, r, "Betrag", r.FormValue("amount"), maxDecimalLen) {
		redirect(w, r, back)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.serverError(w, "anzahlung: year", err)
		return
	}
	// An Abschlag anticipates the Schlussrechnung; once that exists there is
	// nothing left to anticipate, and a second document requesting money would
	// only confuse what is owed.
	if _, err := s.store.GetInvoice(r.Context(), yearID, neighborID); err == nil {
		s.setFlash(w, r, "error", "Es gibt bereits eine festgeschriebene Rechnung — ein Abschlag ist nicht mehr sinnvoll.")
		redirect(w, r, back)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "anzahlung: lookup", err)
		return
	}
	amount := formDecimal(r, "amount").Abs()
	if models.HasSubCent(amount) {
		s.setFlash(w, r, "error", msgMoneyCents)
		redirect(w, r, back)
		return
	}
	av, err := s.store.CreateAnzahlung(r.Context(), yearID, neighborID, year.Year,
		amount, label, parsePaidOn(r.FormValue("due_on")))
	switch {
	case errors.Is(err, store.ErrAmountRequired):
		s.setFlash(w, r, "error", "Bitte einen Betrag größer 0 eingeben.")
	case err != nil:
		s.setFlash(w, r, "error", "Abschlag konnte nicht erstellt werden.")
	default:
		s.setFlash(w, r, "success", "Abschlag "+av.Number+" erstellt.")
	}
	redirect(w, r, back)
}

// handleDocumentStorno cancels one Abschlag or free credit note by id.
func (s *Server) handleDocumentStorno(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	reason := trimmed(r, "reason")
	if s.tooLong(w, r, "Grund", reason, maxNoteLen) {
		redirect(w, r, "/")
		return
	}
	sv, err := s.store.StornoDocument(r.Context(), id, reason)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if errors.Is(err, store.ErrYearCompleted) {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen — für eine Korrektur bitte zuerst wieder öffnen.")
		redirect(w, r, "/")
		return
	}
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", sv.NeighborID, sv.BillingYearID)
	if err != nil {
		s.setFlash(w, r, "error", "Storno fehlgeschlagen.")
		redirect(w, r, "/")
		return
	}
	s.setFlash(w, r, "success", "Beleg storniert ("+sv.Number+").")
	redirect(w, r, back)
}

// handleJournalPDF serves every invoice of a year as ONE document, a page each
// (Ausbaukarte 98) — what an operator hands to the tax adviser or prints in one
// go. The ZIP next to it (Nr. 50) stays the archive of separate files.
func (s *Server) handleJournalPDF(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	docs, err := s.store.ListInvoiceDocs(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Invoices only: a Sammel-PDF of the year's invoices is what is asked for,
	// and mixing storni and credit notes into the same stack would make the
	// stack unusable as a set of documents to hand over.
	list := make([]*models.Invoice, 0, len(docs))
	for i := range docs {
		if docs[i].Kind == "invoice" {
			list = append(list, &docs[i])
		}
	}
	blob, skipped, err := pdf.RenderInvoices(list)
	if err != nil {
		s.setFlash(w, r, "error", "Keine festgeschriebene Rechnung mit Snapshot in diesem Jahr.")
		redirect(w, r, "/rechnungsjournal?year="+itoa64(year.ID))
		return
	}
	if len(skipped) > 0 {
		// Legacy rows without a reconstructible snapshot: say so rather than
		// letting the operator believe the stack is complete.
		slog.Warn("sammel-pdf skipped documents without a snapshot",
			"year", year.Year, "numbers", sanitizeLog(strings.Join(skipped, ",")))
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"rechnungen-%d.pdf\"", year.Year))
	_, _ = w.Write(blob)
}
