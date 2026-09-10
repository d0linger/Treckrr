package server

import (
	"context"
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
	// Title from the single source (models.DunningStageTitle) — the history
	// list's stageName template func reads the same switch, so a renamed stage
	// cannot drift between letter and list.
	title = models.DunningStageTitle(stage)
	switch stage {
	case 1:
		return title, "Trotz unserer Zahlungserinnerung ist der folgende Betrag noch offen. Wir bitten Sie, den Ausgleich umgehend vorzunehmen."
	case 2:
		return title, "Der folgende Betrag ist weiterhin offen. Bitte begleichen Sie ihn unverzüglich, um weitere Schritte zu vermeiden."
	default:
		return title, "Vermutlich haben Sie es übersehen – der folgende Betrag ist noch offen. Bitte gleichen Sie ihn bei Gelegenheit aus."
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
	term := company.EffectiveTermDays()
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
	term := company.EffectiveTermDays()
	rows, err := s.store.DunningRows(r.Context(), year.ID, term, time.Now())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	cw, finish := csvDownload(w, r, fmt.Sprintf("treckrr_mahnwesen_%d.csv", year.Year))
	defer finish()
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
	return s.buildMahnungDataWith(r, neighborID, stage, company, year)
}

// buildMahnungDataWith is buildMahnungData with the loop-invariant company and
// year already in hand — the batch run resolves 30 neighbors and refetched the
// same two rows 30 times each before this split.
func (s *Server) buildMahnungDataWith(r *http.Request, neighborID int64, stage int, company models.Company, year *models.BillingYear) (*mahnungView, bool, error) {
	yearID := year.ID
	neighbor, err := s.store.GetNeighbor(r.Context(), neighborID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, false, nil
	} else if err != nil {
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
	term := company.EffectiveTermDays()
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
	// Nachfrist: a CONFIGURED 0 means "zahlbar sofort" — the letter then names
	// no new deadline at all. Only the letter changes; the column's default (14)
	// covers the unconfigured case, so no code-side fallback is needed. The old
	// grace<=0→14 silently overrode an explicit 0 the settings form accepts.
	if company.DunningGraceDays > 0 {
		v.GraceUntil = time.Now().AddDate(0, 0, company.DunningGraceDays)
	}
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
	status, err := s.deliverMahnung(r.Context(), r, v, "")
	if err != nil {
		s.serverError(w, "mahnung email: pdf", err)
		return
	}
	switch status {
	case "sent":
		s.setFlash(w, r, "success", v.Title+" an "+v.Neighbor.Email+" gesendet.")
	case "sentNoTrail":
		s.setFlash(w, r, "success", v.Title+" an "+v.Neighbor.Email+" gesendet (Versand-Historie konnte nicht gespeichert werden).")
	case "queued":
		s.setFlash(w, r, "error", "Versand fehlgeschlagen — zur automatischen Wiederholung eingeplant.")
	default:
		s.setFlash(w, r, "error", "Versand fehlgeschlagen.")
	}
	redirect(w, r, back)
}

// deliverMahnung is the one delivery tail for a reminder — PDF, body,
// attachment, SMTP, and on failure the retry-outbox park (with the meta the
// outbox needs to write the Mahnhistorie when the retry finally lands); on
// success the CC copy and the dunning/send trail. Single send and Sammellauf
// both call it — the two hand-written copies had already drifted (the batch
// never wrote RecordBelegSend, so the year-closing "nie versendet" check
// reported batch-dunned neighbors as never contacted).
//
// Returns: "sent", "sentNoTrail" (delivered, trail write failed), "queued"
// (parked for retry) or "failed" (send AND park failed); err only for the PDF.
func (s *Server) deliverMahnung(ctx context.Context, r *http.Request, v *mahnungView, auditSuffix string) (string, error) {
	blob, err := v.toPDF()
	if err != nil {
		return "", err
	}
	// Audits ride on the given ctx, not the request's: the batch runs on a
	// WithoutCancel context so a closed browser tab cannot lose the trail of
	// mails that DID go out.
	ar := r.WithContext(ctx)
	body := mailBody(v.Company, v.Neighbor.Name, "anbei "+v.Title+" zur Rechnung "+v.Invoice.Number+" als PDF.")
	att := mail.Attachment{Filename: "Mahnung_" + sanitizeFilename(v.Invoice.Number) + ".pdf", ContentType: "application/pdf", Data: blob}
	subject := v.Title + " · Rechnung " + v.Invoice.Number
	if err := mail.Send(ctx, s.cfg, v.Neighbor.Email, subject, body, []mail.Attachment{att}); err != nil {
		metrics.Inc(metrics.MailFailed)
		s.audit(ar, "mahnung_email_failed", "neighbor", v.Neighbor.ID,
			v.Neighbor.Name+" · "+v.Title+" · Rechnung "+v.Invoice.Number+" · "+err.Error()+auditSuffix)
		slog.Error("mahnung email send failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(err.Error()))
		if qerr := s.store.EnqueueMail(ctx, store.OutboxMail{
			Kind: "mahnung", NeighborID: v.Neighbor.ID, BillingYearID: v.Invoice.BillingYearID,
			Recipient: v.Neighbor.Email, Subject: subject, Body: body,
			AttName: att.Filename, AttType: att.ContentType, AttData: att.Data,
			Meta: store.OutboxMeta{Stage: v.Stage, Fee: v.Fee, GraceUntil: v.GraceUntil, InvoiceNumber: v.Invoice.Number},
		}); qerr != nil {
			slog.Error("mahnung email enqueue failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(qerr.Error()))
			return "failed", nil
		}
		return "queued", nil
	}
	// Delivered — the configured CC gets its copy (Nr. 99), best-effort.
	s.sendMailCopy(ctx, v.Company, subject, body, []mail.Attachment{att})
	trailOK := true
	if err := s.store.RecordDunningNotice(ctx, store.DunningNotice{
		BillingYearID: v.Invoice.BillingYearID, NeighborID: v.Neighbor.ID,
		InvoiceNumber: v.Invoice.Number, Stage: v.Stage, Channel: "e-mail",
		GraceUntil: v.GraceUntil, Fee: v.Fee,
	}); err != nil {
		slog.Error("record dunning notice failed", "neighbor", v.Neighbor.ID, "err", sanitizeLog(err.Error()))
		trailOK = false
	}
	if err := s.store.RecordBelegSend(ctx, v.Year.ID, v.Neighbor.ID, "mahnung"); err != nil {
		slog.Error("record beleg send failed", "kind", "mahnung", "year", v.Year.ID, "neighbor", v.Neighbor.ID, "err", err)
		trailOK = false
	}
	s.audit(ar, "mahnung_email", "neighbor", v.Neighbor.ID, v.Neighbor.Name+" · E-Mail · "+v.Title+" "+v.Invoice.Number+auditSuffix)
	if !trailOK {
		return "sentNoTrail", nil
	}
	return "sent", nil
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
	term := company.EffectiveTermDays()
	rows, err := s.store.DunningRows(r.Context(), yearID, term, time.Now())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// A batch of SMTP dialogs does not fit the server's 30s write timeout, and
	// once the operator kicked the run off, a closed tab must not strand the
	// remaining letters half-sent with their trail unwritten: the deadline is
	// extended and the loop runs detached from the request's cancellation.
	extendWriteDeadline(w, 10*time.Minute)
	bctx := context.WithoutCancel(r.Context())

	var sent, queued, skipped, failed int
	for _, row := range rows {
		// Company and year are loop-invariant (see buildMahnungDataWith); only
		// the per-neighbor rows are fetched inside the loop.
		v, ok, err := s.buildMahnungDataWith(r.WithContext(bctx), row.NeighborID, stage, company, year)
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
		status, err := s.deliverMahnung(bctx, r, v, " (Sammellauf)")
		if err != nil {
			s.serverError(w, "mahnwesen batch: pdf", err)
			return
		}
		switch status {
		case "sent", "sentNoTrail":
			sent++
		case "queued":
			queued++
		default:
			// Send AND retry-park failed — this neighbor HAS an address; counting
			// them under "ohne E-Mail-Adresse übersprungen" told the operator a lie.
			failed++
		}
	}

	msg := fmt.Sprintf("Sammel-Mahnlauf: %d gesendet", sent)
	if queued > 0 {
		msg += fmt.Sprintf(", %d zur Wiederholung eingeplant", queued)
	}
	if failed > 0 {
		msg += fmt.Sprintf(", %d fehlgeschlagen (bitte erneut versuchen)", failed)
	}
	if skipped > 0 {
		msg += fmt.Sprintf(", %d ohne E-Mail-Adresse übersprungen", skipped)
	}
	kind := "success"
	if failed > 0 {
		kind = "error"
	}
	s.setFlash(w, r, kind, msg+".")
	redirect(w, r, back)
}
