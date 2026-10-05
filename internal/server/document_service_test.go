package server

import (
	"bytes"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestDocumentServiceBuildsInvoiceMailFromFrozenSnapshot(t *testing.T) {
	invoice := models.Invoice{
		ID: 17, Number: "2026-017", IssuedOn: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC),
		Content: &models.InvoiceContent{
			Net: decimal.RequireFromString("107.87"), Gross: decimal.RequireFromString("107.87"),
			ServiceFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			ServiceTo:   time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			Issuer:      models.InvoiceParty{Name: "Betrieb", Address: "Feldweg 1"},
			Recipient:   models.InvoiceParty{Name: "Nachbar", Address: "Dorf 2"},
			Lines: []models.InvoiceLine{{
				Date:  time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC),
				Label: "Mähen", Unit: "h", Quantity: decimal.RequireFromString("2.345"),
				UnitPrice: decimal.RequireFromString("46.00"), Cost: decimal.RequireFromString("107.87"),
			}},
		},
	}
	neighbor := models.Neighbor{ID: 9, Name: "Nachbar", Email: "NACHBAR@example.at"}
	document, err := (documentService{smtpFrom: "rechnung@example.at"}).invoiceMail(
		models.Company{Name: "Betrieb"},
		neighbor,
		42,
		invoice,
	)
	if err != nil {
		t.Fatal(err)
	}
	if document.Subject != "Rechnung 2026-017" || document.Outbox.Subject != document.Subject {
		t.Fatalf("subject = %q/%q, want shared invoice subject", document.Subject, document.Outbox.Subject)
	}
	if document.Outbox.DeliveryKey != "beleg:17:nachbar@example.at" || document.Outbox.BillingYearID != 42 {
		t.Fatalf("outbox identity = %+v", document.Outbox)
	}
	if document.Attachment.Filename != "Rechnung_2026-017.pdf" ||
		document.Outbox.AttName != document.Attachment.Filename ||
		!bytes.HasPrefix(document.Attachment.Data, []byte("%PDF-")) {
		t.Fatalf("attachment = %q (%d bytes), want shared rendered invoice PDF",
			document.Attachment.Filename, len(document.Attachment.Data))
	}
}
