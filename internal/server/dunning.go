package server

import (
	"encoding/csv"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// dunningStage maps a stage code to its German document heading + intro line.
// Stage 0 = friendly reminder (no fee), 1/2 = escalating Mahnungen. Chosen at
// print time; no state is persisted.
func dunningStage(stage int) (title, intro string) {
	switch stage {
	case 1:
		return "1. Mahnung", "Trotz unserer Zahlungserinnerung ist der folgende Betrag noch offen. Wir bitten Sie, den Ausgleich umgehend vorzunehmen."
	case 2:
		return "2. Mahnung", "Der folgende Betrag ist weiterhin offen. Bitte begleichen Sie ihn unverzüglich, um weitere Schritte zu vermeiden."
	default:
		return "Zahlungserinnerung", "Vermutlich haben Sie es übersehen – der folgende Betrag ist noch offen. Bitte gleichen Sie ihn bei Gelegenheit aus."
	}
}

// handleMahnwesen renders the dunning list: neighbors in the selected billing
// year whose issued invoice is unpaid and past due (issue date + payment term).
func (s *Server) handleMahnwesen(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// A configured term of 0 (due immediately) is valid; only a negative value —
	// which the settings form never stores — falls back to the default.
	term := company.PaymentTermDays
	if term < 0 {
		term = 14
	}
	// scope=alle switches to the cross-year open-items list (Nr. 37): the same
	// overdue definition, but yearID 0 pulls every year and the rows carry their
	// year so the Altersstaffel (30/60/90 tags in the template) means something.
	scopeYear := year.ID
	allYears := r.URL.Query().Get("scope") == "alle"
	if allYears {
		scopeYear = 0
	}
	rows, err := s.store.DunningRows(r.Context(), scopeYear, term, time.Now())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	lastNotices, err := s.store.LastDunningNotices(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	data := s.newPage(w, r, "Mahnwesen", "mahnwesen")
	data["Year"] = year
	data["Rows"] = rows
	data["Term"] = term
	data["AllYears"] = allYears
	data["LastNotices"] = lastNotices
	data["MailEnabled"] = s.cfg.MailEnabled()
	s.render(w, r, "mahnwesen", data)
}

// handleMahnwesenExport exports the overdue list of the selected year as a
// German-locale, semicolon-separated CSV (BOM + csvSafe), for the bookkeeping.
// Same overdue definition as the on-screen list (DunningRows).
func (s *Server) handleMahnwesenExport(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	term := company.PaymentTermDays
	if term < 0 {
		term = 14
	}
	rows, err := s.store.DunningRows(r.Context(), year.ID, term, time.Now())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fmt.Sprintf("treckrr_mahnwesen_%d.csv", year.Year)+"\"")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM for Excel
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	defer func() {
		cw.Flush()
		if err := cw.Error(); err != nil {
			// The status is long gone — logging is what is still possible; without
			// it an aborted download is a silently truncated file behind HTTP 200.
			slog.Warn("csv export incomplete", "path", sanitizeLog(r.URL.Path), "err", sanitizeLog(err.Error()))
		}
	}()
	_ = cw.Write([]string{"Nachbar", "Rechnung", "Rechnungsdatum", "Fällig am", "Tage überfällig", "Offener Betrag (€)"})
	for _, dr := range rows {
		_ = cw.Write([]string{
			csvSafe(dr.Name),
			csvSafe(dr.InvoiceNo),
			dr.IssuedOn.Format("02.01.2006"),
			dr.DueOn.Format("02.01.2006"),
			strconv.Itoa(dr.DaysOverdue),
			deDecimal(dr.Open), // German decimal "1234,50", same as the other CSV exports
		})
	}
}

// handleNeighborMahnung renders a printable reminder/dunning letter for one
// neighbor. The stage (0 = Zahlungserinnerung, 1/2 = Mahnungen) is chosen at
// print time and sets the heading and wording — no state is persisted.
// mahnungView is the fully-resolved reminder, shared by the HTML page, the PDF and
// the e-mail send so all three show the same figures.
type mahnungView struct {
	Neighbor     *models.Neighbor
	Year         *models.BillingYear
	Company      models.Company
	Invoice      models.Invoice
	Title, Intro string
	Stage        int
	Open, Paid   decimal.Decimal
	// Fee is the stage's Mahnspesen (0 = none), TotalDue = Open + Fee, and
	// GraceUntil is the Nachfrist printed as "zahlbar bis".
	Fee, TotalDue decimal.Decimal
	GraceUntil    time.Time
	DueOn         time.Time
	HasEpcQR      bool
}

// buildMahnungData resolves a reminder for a neighbor+year+stage. ok=false when
// there is nothing to dun (no neighbor/year/issued invoice → caller 404s).
func (s *Server) buildMahnungData(r *http.Request, neighborID, yearID int64, stage int) (*mahnungView, bool, error) {
	// (nil, false, nil) means "no such reminder" → 404; a real DB error must
	// propagate as (nil, false, err) → 500, not be masked as not-found.
	neighbor, err := s.store.GetNeighbor(r.Context(), neighborID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		return nil, false, err
	}
	// A reminder only makes sense for a formally issued invoice.
	iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
		return nil, false, err
	}
	// Open = amount STILL PAYABLE on the frozen invoice (gross less credits, ledger,
	// payments) — same figure the Beleg/EPC-QR use. paid = payments total (info line).
	open, err := s.store.InvoiceRemaining(r.Context(), yearID, neighborID)
	if err != nil {
		return nil, false, err
	}
	_, paid, err := s.store.NeighborNetPaid(r.Context(), yearID, neighborID)
	if err != nil {
		return nil, false, err
	}
	title, intro := dunningStage(stage)
	term := company.PaymentTermDays
	if term < 0 {
		term = 14
	}
	// The neighbor's own payment term wins over the company default (Nr. 36).
	if neighbor.PaymentTermDays != nil {
		term = *neighbor.PaymentTermDays
	}
	v := &mahnungView{
		Neighbor: neighbor, Year: year, Company: company, Invoice: iv,
		Title: title, Intro: intro, Stage: stage, Open: open, Paid: paid,
		HasEpcQR: strings.TrimSpace(company.IBAN) != "" && open.IsPositive(),
	}
	// Mahnspesen per stage (default 0 = letter unchanged); the reminder (stage 0)
	// never charges. TotalDue is what the payment block asks for.
	switch stage {
	case 1:
		v.Fee = company.DunningFee1
	case 2:
		v.Fee = company.DunningFee2
	}
	v.TotalDue = open.Add(v.Fee)
	// Nachfrist: a Mahnung without a new deadline only points at the missed one.
	grace := company.DunningGraceDays
	if grace <= 0 {
		grace = 14
	}
	v.GraceUntil = time.Now().AddDate(0, 0, grace)
	if !iv.IssuedOn.IsZero() {
		v.DueOn = iv.IssuedOn.AddDate(0, 0, term)
	}
	return v, true, nil
}

func (s *Server) handleNeighborMahnung(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID := formInt64(r, "year")
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	// formInt parses via strconv.Atoi (no lossy int64->int narrowing); unknown
	// values fall through dunningStage's default (Zahlungserinnerung).
	v, ok, err := s.buildMahnungData(r, neighborID, yearID, formInt(r, "stufe"))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	data := s.newPage(w, r, v.Title, "")
	data["Neighbor"] = v.Neighbor
	data["Year"] = v.Year
	data["Today"] = time.Now()
	data["Company"] = v.Company
	data["Title"] = v.Title
	data["Intro"] = v.Intro
	data["Stage"] = v.Stage
	data["Open"] = v.Open
	data["Paid"] = v.Paid
	data["Fee"] = v.Fee
	data["TotalDue"] = v.TotalDue
	data["GraceUntil"] = v.GraceUntil
	data["InvoiceNo"] = v.Invoice.Number
	data["IssuedOn"] = v.Invoice.IssuedOn
	if !v.DueOn.IsZero() {
		data["DueOn"] = v.DueOn
	}
	data["HasEpcQR"] = v.HasEpcQR
	data["MailEnabled"] = s.cfg.MailEnabled()
	data["NeighborEmail"] = v.Neighbor.Email
	s.render(w, r, "mahnung", data)
}

// mahnungPDF renders the reminder view to a PDF.
func (v *mahnungView) toPDF() ([]byte, error) {
	return pdf.RenderMahnung(pdf.MahnungData{
		IssuerName: v.Company.Name, IssuerAddress: v.Company.Address, IssuerIBAN: v.Company.IBAN,
		RecipientName: v.Neighbor.Name, RecipientAddr: v.Neighbor.Address,
		Title: v.Title, Intro: v.Intro, InvoiceNo: v.Invoice.Number,
		IssuedOn: v.Invoice.IssuedOn, DueOn: v.DueOn, Open: v.Open, Paid: v.Paid,
		Fee: v.Fee, GraceUntil: v.GraceUntil, Today: time.Now(),
	})
}

// handleMahnungPDF serves the reminder as a PDF.
func (s *Server) handleMahnungPDF(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, ok, err := s.buildMahnungData(r, neighborID, formInt64(r, "year"), formInt(r, "stufe"))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	blob, err := v.toPDF()
	if err != nil {
		s.serverError(w, "mahnung pdf", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="Mahnung_`+sanitizeFilename(v.Invoice.Number)+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}

// handleMahnungEmail sends the reminder PDF to the neighbor's e-mail.
func (s *Server) handleMahnungEmail(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID := formInt64(r, "year")
	back := fmt.Sprintf("/neighbors/%d/mahnung?year=%d&stufe=%d", neighborID, yearID, formInt(r, "stufe"))
	v, ok, err := s.buildMahnungData(r, neighborID, yearID, formInt(r, "stufe"))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !s.cfg.MailEnabled() {
		s.setFlash(w, r, "error", "E-Mail-Versand ist nicht konfiguriert (SMTP_HOST/SMTP_FROM).")
		redirect(w, r, back)
		return
	}
	if strings.TrimSpace(v.Neighbor.Email) == "" {
		s.setFlash(w, r, "error", "Für "+v.Neighbor.Name+" ist keine E-Mail-Adresse hinterlegt.")
		redirect(w, r, back)
		return
	}
	blob, err := v.toPDF()
	if err != nil {
		s.serverError(w, "mahnung email: pdf", err)
		return
	}
	from := strings.TrimSpace(v.Company.Name)
	if from == "" {
		from = "Ihr Maschinenring"
	}
	body := mailBody(v.Company, v.Neighbor.Name, "anbei "+v.Title+" zur Rechnung "+v.Invoice.Number+" als PDF.", from)
	att := mail.Attachment{Filename: "Mahnung_" + sanitizeFilename(v.Invoice.Number) + ".pdf", ContentType: "application/pdf", Data: blob}
	subject := v.Title + " · Rechnung " + v.Invoice.Number
	if err := mail.Send(r.Context(), s.cfg, v.Neighbor.Email, subject, body, []mail.Attachment{att}); err != nil {
		metrics.Inc(metrics.MailFailed)
		s.audit(r, "mahnung_email_failed", "neighbor", v.Neighbor.ID,
			v.Neighbor.Name+" · "+v.Title+" · Rechnung "+v.Invoice.Number+" · "+err.Error())
		if qerr := s.store.EnqueueMail(r.Context(), store.OutboxMail{
			Kind: "mahnung", NeighborID: v.Neighbor.ID, BillingYearID: v.Invoice.BillingYearID,
			Recipient: v.Neighbor.Email, Subject: v.Title + " · Rechnung " + v.Invoice.Number, Body: body,
			AttName: att.Filename, AttType: att.ContentType, AttData: att.Data,
		}); qerr != nil {
			slog.Error("mahnung email enqueue failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(qerr.Error()))
		}
		slog.Error("mahnung email send failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Versand fehlgeschlagen.")
		redirect(w, r, back)
		return
	}
	// Delivery succeeded; the send-trail marker is secondary — mirror handleBelegEmail:
	// log a failed write and tell the user the reminder went out but wasn't recorded.
	// The configured CC gets its copy (Nr. 99), best-effort.
	s.sendMailCopy(r.Context(), v.Company, subject, body, []mail.Attachment{att})
	if err := s.store.RecordDunningNotice(r.Context(), store.DunningNotice{
		BillingYearID: v.Invoice.BillingYearID, NeighborID: v.Neighbor.ID,
		InvoiceNumber: v.Invoice.Number, Stage: v.Stage, Channel: "e-mail",
		GraceUntil: v.GraceUntil, Fee: v.Fee,
	}); err != nil {
		slog.Error("record dunning notice failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(err.Error()))
	}
	if err := s.store.RecordBelegSend(r.Context(), v.Year.ID, v.Neighbor.ID, "mahnung"); err != nil {
		slog.Error("record beleg send failed", "kind", "mahnung", "year", v.Year.ID, "neighbor", v.Neighbor.ID, "err", err)
		s.audit(r, "mahnung_email", "neighbor", v.Neighbor.ID, v.Neighbor.Name+" · E-Mail · "+v.Title+" "+v.Invoice.Number)
		s.setFlash(w, r, "success", v.Title+" an "+v.Neighbor.Email+" gesendet (Versand-Historie konnte nicht gespeichert werden).")
		redirect(w, r, back)
		return
	}
	s.audit(r, "mahnung_email", "neighbor", v.Neighbor.ID, v.Neighbor.Name+" · E-Mail · "+v.Title+" "+v.Invoice.Number)
	s.setFlash(w, r, "success", v.Title+" an "+v.Neighbor.Email+" gesendet.")
	redirect(w, r, back)
}

// handleMahnungEpcQR serves the EPC/GiroCode QR for a reminder, encoding the
// remaining payable on the issued invoice (gross less credits/ledger/payments)
// PLUS the stage's Mahnspesen, i.e. the same figure the letter and the PDF ask
// for. Encoding the bare open amount would hand the debtor a code that pays less
// than the document beside it demands, leaving the fee open after they paid.
func (s *Server) handleMahnungEpcQR(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID := formInt64(r, "year")
	if yearID == 0 {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil || strings.TrimSpace(company.IBAN) == "" {
		http.NotFound(w, r)
		return
	}
	// Require a formally issued invoice: the QR carries its number as the payment
	// reference, and there is nothing to dun without one. Mirrors handleNeighborMahnung.
	iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	open, err := s.store.InvoiceRemaining(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !open.IsPositive() {
		http.NotFound(w, r)
		return
	}
	// Same stage → same fee as buildMahnungData; stage 0 (Erinnerung) never charges.
	total := open
	switch formInt(r, "stufe") {
	case 1:
		total = total.Add(company.DunningFee1)
	case 2:
		total = total.Add(company.DunningFee2)
	}
	png, err := qrPNG(epcPayload(company.Name, company.IBAN, total, iv.Number))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

// handleMahnungMarkSent records a reminder that left the house OUTSIDE the app
// (printed and posted, handed over in person). Without it the history only knew
// about e-mails, and the list's "Zuletzt:" line lied for everyone who prints.
func (s *Server) handleMahnungMarkSent(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := formInt64(r, "year")
	back := fmt.Sprintf("/mahnwesen?year=%d", yearID)
	v, ok, err := s.buildMahnungData(r, neighborID, yearID, formInt(r, "stufe"))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := s.store.RecordDunningNotice(r.Context(), store.DunningNotice{
		BillingYearID: v.Invoice.BillingYearID, NeighborID: v.Neighbor.ID,
		InvoiceNumber: v.Invoice.Number, Stage: v.Stage, Channel: "manuell",
		GraceUntil: v.GraceUntil, Fee: v.Fee,
	}); err != nil {
		s.serverError(w, "mahnung mark-sent", err)
		return
	}
	s.audit(r, "mahnung_marked_sent", "neighbor", v.Neighbor.ID,
		v.Neighbor.Name+" · "+v.Title+" · Rechnung "+v.Invoice.Number)
	s.setFlash(w, r, "success", v.Title+" für "+v.Neighbor.Name+" im Verlauf vermerkt.")
	redirect(w, r, back)
}

// handleMahnwesenBatchEmail sends one chosen stage to every overdue neighbor
// with an e-mail address (Nr. 35). Neighbors without one are skipped and named
// in the summary; a failed send is parked in the outbox like the single-send
// path, so the batch never silently loses anyone.
func (s *Server) handleMahnwesenBatchEmail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := formInt64(r, "year")
	stage := formInt(r, "stufe")
	back := fmt.Sprintf("/mahnwesen?year=%d", yearID)
	if !s.cfg.MailEnabled() {
		s.setFlash(w, r, "error", "E-Mail-Versand ist nicht konfiguriert (SMTP_HOST/SMTP_FROM).")
		redirect(w, r, back)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	term := company.PaymentTermDays
	if term < 0 {
		term = 14
	}
	rows, err := s.store.DunningRows(r.Context(), yearID, term, time.Now())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	var sent, queued, skipped int
	for _, row := range rows {
		v, ok, err := s.buildMahnungData(r, row.NeighborID, yearID, stage)
		if err != nil {
			s.serverError(w, "mahnwesen batch", err)
			return
		}
		if !ok {
			continue
		}
		if strings.TrimSpace(v.Neighbor.Email) == "" {
			skipped++
			continue
		}
		blob, err := v.toPDF()
		if err != nil {
			s.serverError(w, "mahnwesen batch: pdf", err)
			return
		}
		from := strings.TrimSpace(v.Company.Name)
		if from == "" {
			from = "Ihr Maschinenring"
		}
		body := mailBody(v.Company, v.Neighbor.Name, "anbei "+v.Title+" zur Rechnung "+v.Invoice.Number+" als PDF.", from)
		att := mail.Attachment{Filename: "Mahnung_" + sanitizeFilename(v.Invoice.Number) + ".pdf", ContentType: "application/pdf", Data: blob}
		subject := v.Title + " · Rechnung " + v.Invoice.Number
		if err := mail.Send(r.Context(), s.cfg, v.Neighbor.Email, subject, body, []mail.Attachment{att}); err != nil {
			metrics.Inc(metrics.MailFailed)
			s.audit(r, "mahnung_email_failed", "neighbor", v.Neighbor.ID,
				v.Neighbor.Name+" · "+v.Title+" · Rechnung "+v.Invoice.Number+" · "+err.Error())
			if qerr := s.store.EnqueueMail(r.Context(), store.OutboxMail{
				Kind: "mahnung", NeighborID: v.Neighbor.ID, BillingYearID: v.Invoice.BillingYearID,
				Recipient: v.Neighbor.Email, Subject: v.Title + " · Rechnung " + v.Invoice.Number, Body: body,
				AttName: att.Filename, AttType: att.ContentType, AttData: att.Data,
			}); qerr != nil {
				slog.Error("mahnwesen batch: enqueue failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(qerr.Error()))
				skipped++
				continue
			}
			queued++
			continue
		}
		// Delivered — the configured CC gets its copy (Nr. 99), best-effort.
		s.sendMailCopy(r.Context(), v.Company, subject, body, []mail.Attachment{att})
		if err := s.store.RecordDunningNotice(r.Context(), store.DunningNotice{
			BillingYearID: v.Invoice.BillingYearID, NeighborID: v.Neighbor.ID,
			InvoiceNumber: v.Invoice.Number, Stage: v.Stage, Channel: "e-mail",
			GraceUntil: v.GraceUntil, Fee: v.Fee,
		}); err != nil {
			slog.Error("mahnwesen batch: record notice failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(err.Error()))
		}
		s.audit(r, "mahnung_email", "neighbor", v.Neighbor.ID,
			v.Neighbor.Name+" · "+v.Title+" · Rechnung "+v.Invoice.Number+" (Sammellauf)")
		sent++
	}

	msg := fmt.Sprintf("Sammel-Mahnlauf: %d gesendet", sent)
	if queued > 0 {
		msg += fmt.Sprintf(", %d zur Wiederholung eingeplant", queued)
	}
	if skipped > 0 {
		msg += fmt.Sprintf(", %d ohne E-Mail-Adresse übersprungen", skipped)
	}
	s.setFlash(w, r, "success", msg+".")
	redirect(w, r, back)
}
