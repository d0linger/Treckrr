package server

import (
	"fmt"
	"strings"

	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// documentService assembles delivery artifacts exclusively from immutable
// invoice snapshots. HTML, PDF, and mail therefore share models.Invoice as the
// document source while live settlement remains outside the frozen content.
type documentService struct {
	smtpFrom string
}

type invoiceMailDocument struct {
	Subject    string
	Body       string
	Attachment mail.Attachment
	Outbox     store.OutboxMail
}

func (s *Server) documents() documentService {
	return documentService{smtpFrom: s.cfg.SMTPFrom}
}

func (documentService) invoicePDF(invoice models.Invoice) ([]byte, error) {
	blob, err := pdf.RenderInvoice(&invoice)
	if err != nil {
		return nil, fmt.Errorf("render invoice pdf: %w", err)
	}
	return blob, nil
}

func (s documentService) invoiceMail(
	company models.Company,
	neighbor models.Neighbor,
	yearID int64,
	invoice models.Invoice,
) (invoiceMailDocument, error) {
	blob, err := s.invoicePDF(invoice)
	if err != nil {
		return invoiceMailDocument{}, err
	}
	subject := "Rechnung " + invoice.Number
	body := mailBody(company, neighbor.Name, "anbei die Rechnung "+invoice.Number+" als PDF.")
	attachment := mail.Attachment{
		Filename:    "Rechnung_" + sanitizeFilename(invoice.Number) + ".pdf",
		ContentType: "application/pdf",
		Data:        blob,
	}
	messageID := mail.StableMessageID(
		s.smtpFrom,
		neighbor.Email,
		subject,
		body,
		[]mail.Attachment{attachment},
	)
	return invoiceMailDocument{
		Subject:    subject,
		Body:       body,
		Attachment: attachment,
		Outbox: store.OutboxMail{
			Kind: "beleg", NeighborID: neighbor.ID, BillingYearID: yearID,
			Recipient: neighbor.Email, Subject: subject, Body: body,
			AttName: attachment.Filename, AttType: attachment.ContentType, AttData: attachment.Data,
			DeliveryKey: fmt.Sprintf("beleg:%d:%s", invoice.ID,
				strings.ToLower(strings.TrimSpace(neighbor.Email))),
			MessageID: messageID, RetryFailed: true, ForceResend: true,
		},
	}, nil
}
