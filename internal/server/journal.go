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
	"strings"

	"github.com/shopspring/decimal"

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

// writeJournalCSV writes the journal in the export CSV dialect (BOM + ';').
func writeJournalCSV(w *csv.Writer, rows []store.JournalRow) {
	_ = w.Write([]string{"Nummer", "Datum", "Art", "Status", "Nachbar", "Netto (€)", "USt-Satz (%)", "USt (€)", "Brutto (€)"})
	kinds := map[string]string{"invoice": "Rechnung", "storno": "Storno", "gutschrift": "Gutschrift", "anzahlung": "Abschlag"}
	status := map[string]string{"issued": "ausgestellt", "canceled": "storniert"}
	for _, j := range rows {
		_ = w.Write([]string{
			j.Number, j.IssuedOn.Format("02.01.2006"), orKey(kinds, j.Kind), orKey(status, j.Status), j.NeighborName,
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
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	writeJournalCSV(cw, rows)
	cw.Flush()
	if err := cw.Error(); err != nil {
		slog.Warn("journal csv incomplete", "err", sanitizeLog(err.Error()))
	}
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
		http.NotFound(w, r)
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
	gv, err := s.store.FreeGutschrift(r.Context(), yearID, neighborID, year.Year, amount, note)
	switch {
	case errors.Is(err, store.ErrAmountRequired):
		s.setFlash(w, r, "error", "Bitte einen Betrag größer 0 eingeben.")
	case err != nil:
		s.setFlash(w, r, "error", "Gutschrift fehlgeschlagen.")
	default:
		s.audit(r, "invoice_gutschrift", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · freie Gutschrift "+gv.Number+" · "+amount.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Gutschrift "+gv.Number+" erstellt.")
	}
	redirect(w, r, back)
}

// handleAnzahlungCreate issues an Abschlag for a partial payment during the
// season (Ausbaukarte 54).
func (s *Server) handleAnzahlungCreate(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
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
	av, err := s.store.CreateAnzahlung(r.Context(), yearID, neighborID, year.Year,
		amount, label, parsePaidOn(r.FormValue("due_on")))
	switch {
	case errors.Is(err, store.ErrAmountRequired):
		s.setFlash(w, r, "error", "Bitte einen Betrag größer 0 eingeben.")
	case err != nil:
		s.setFlash(w, r, "error", "Abschlag konnte nicht erstellt werden.")
	default:
		s.audit(r, "anzahlung_create", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · Abschlag "+av.Number+" · "+amount.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Abschlag "+av.Number+" erstellt.")
	}
	redirect(w, r, back)
}

// handleDocumentStorno cancels one Abschlag or free credit note by id.
func (s *Server) handleDocumentStorno(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", sv.NeighborID, sv.BillingYearID)
	if err != nil {
		s.setFlash(w, r, "error", "Storno fehlgeschlagen.")
		redirect(w, r, "/")
		return
	}
	s.audit(r, "document_storno", "neighbor", sv.NeighborID,
		s.neighborName(r, sv.NeighborID)+" · Storno "+sv.Number)
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
