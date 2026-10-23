package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/metrics"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// statementPDF renders the multi-year account statement of one neighbor, the
// same document as the "Als PDF" download.
func (s *Server) statementPDF(ctx context.Context, neighbor *models.Neighbor, company models.Company, today time.Time) ([]byte, error) {
	statement, err := s.store.ReadNeighborStatement(ctx, neighbor.ID)
	if err != nil {
		return nil, fmt.Errorf("statement history: %w", err)
	}
	sd := pdf.StatementData{
		IssuerName: company.Name, IssuerAddress: company.Address,
		RecipientName: neighbor.Name, RecipientAddr: neighbor.Address, Today: today,
	}
	for _, h := range statement.Years {
		sd.Rows = append(sd.Rows, pdf.StatementYear{
			Year: h.Year, Cost: h.Net, Hours: h.Hours, PaidAmount: h.PaidAmount,
			Remaining: h.Remaining, Paid: h.Paid, Credit: h.Credit,
		})
		sd.TotalCost = sd.TotalCost.Add(h.Net)
		sd.TotalHours = sd.TotalHours.Add(h.Hours)
	}
	for _, payment := range statement.Payments {
		sd.Payments = append(sd.Payments, pdf.StatementPayment{Year: payment.Year, Date: payment.PaidOn,
			Amount: payment.Amount, Reference: payment.InvoiceNumber, Reversal: payment.Reversal})
	}
	return pdf.RenderStatement(sd)
}

// handleNeighborOverviewEmail mails the account statement PDF to the neighbor
// through the outbox, like invoices and reminders: one delivery per neighbor,
// address and day, with retry and history.
func (s *Server) handleNeighborOverviewEmail(w http.ResponseWriter, r *http.Request) {
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
	back := "/neighbors/" + itoa64(id) + "/overview"
	if !s.cfg.MailEnabled() {
		s.setFlash(w, r, "error", "E-Mail-Versand ist nicht konfiguriert (SMTP_HOST/SMTP_FROM).")
		redirect(w, r, back)
		return
	}
	address := strings.TrimSpace(neighbor.Email)
	if address == "" {
		s.setFlash(w, r, "error", "Für "+neighbor.Name+" ist keine E-Mail-Adresse hinterlegt.")
		redirect(w, r, back)
		return
	}
	extendWriteDeadline(w, 2*store.MailDeliveryBudget)
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, "statement email: company", err)
		return
	}
	today := time.Now()
	blob, err := s.statementPDF(r.Context(), neighbor, company, today)
	if err != nil {
		s.serverError(w, "statement email: pdf", err)
		return
	}
	subject := "Kontoauszug · Stand " + today.Format("02.01.2006")
	body := mailBody(company, neighbor.Name, "anbei dein aktueller Kontoauszug über alle Abrechnungsjahre als PDF (Stand "+
		today.Format("02.01.2006")+"). Er zeigt Leistungen, Zahlungen und den offenen Betrag je Jahr; Rechnungen bleiben davon unberührt.")
	attachment := mail.Attachment{
		Filename:    "Kontoauszug_" + sanitizeFilename(neighbor.Name) + "_" + today.Format("2006-01-02") + ".pdf",
		ContentType: "application/pdf", Data: blob,
	}
	intent, created, err := s.store.CreateMailIntent(r.Context(), store.OutboxMail{
		Kind: "statement", NeighborID: neighbor.ID, Recipient: address, Subject: subject, Body: body,
		AttName: attachment.Filename, AttType: attachment.ContentType, AttData: attachment.Data,
		DeliveryKey: fmt.Sprintf("statement:%d:%s:%s", neighbor.ID, today.Format("2006-01-02"), strings.ToLower(address)),
		MessageID:   mail.StableMessageID(s.cfg.SMTPFrom, address, subject, body, []mail.Attachment{attachment}),
		RetryFailed: true,
	})
	if err != nil {
		slog.Error("statement email intent failed", "neighbor", neighbor.ID, "err", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Versand fehlgeschlagen.")
		redirect(w, r, back)
		return
	}
	status := intent.Status
	if created {
		status, err = s.store.AttemptMail(r.Context(), intent.ID,
			func(ctx context.Context, messageID, to, subject, body, attName, attType string, attData []byte) error {
				return mail.SendWithMessageID(ctx, s.cfg, to, subject, body, []mail.Attachment{{
					Filename: attName, ContentType: attType, Data: attData,
				}}, messageID)
			})
		if err != nil {
			slog.Error("statement email attempt failed", "neighbor", neighbor.ID, "err", sanitizeLog(err.Error()))
			s.setFlash(w, r, "error", "Versandstatus konnte nicht gespeichert werden. Bitte im Mailausgang prüfen.")
			redirect(w, r, back)
			return
		}
	}
	s.audit(r, "statement_email", "neighbor", neighbor.ID, neighbor.Name+" · Kontoauszug an "+address)
	switch status {
	case store.MailStatusSent:
		if created {
			s.setFlash(w, r, "success", "Kontoauszug an "+address+" gesendet.")
		} else {
			s.setFlash(w, r, "success", "Der Kontoauszug wurde heute bereits an diese Adresse gesendet.")
		}
	case store.MailStatusPending, store.MailStatusSending:
		metrics.Inc(metrics.MailFailed)
		s.setFlash(w, r, "error", "Versand fehlgeschlagen — ein erneuter Versuch wurde eingeplant.")
	case store.MailStatusAmbiguous:
		metrics.Inc(metrics.MailFailed)
		s.setFlash(w, r, "error", "Zustellung unklar — keine automatische Wiederholung. Bitte den Mailausgang prüfen.")
	case store.MailStatusHeld:
		s.setFlash(w, r, "error", heldMailFlash)
	default:
		metrics.Inc(metrics.MailFailed)
		s.setFlash(w, r, "error", "Versand endgültig fehlgeschlagen. Bitte den Mailausgang prüfen.")
	}
	redirect(w, r, back)
}
