package server

import (
	"errors"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/einvoice"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// renderInvoiceDocument is the only HTML adapter for frozen invoices. Public
// links receive only the selected document and its customer-safe reductions;
// private annual comparisons and unrelated documents must never be rendered.
func (s *Server) renderInvoiceDocument(w http.ResponseWriter, r *http.Request,
	neighbor *models.Neighbor, year *models.BillingYear, invoice models.Invoice, token string, shareID int64,
) {
	var view invoiceDocumentView
	var err error
	data := s.newPage(w, r, "Rechnung "+invoice.Number, "dashboard")
	if token == "" {
		account, accountErr := s.store.ReadAccountSettlement(r.Context(), year.ID, neighbor.ID)
		if accountErr != nil {
			s.invoiceRequestError(w, r, accountErr)
			return
		}
		remaining := decimal.Zero
		if invoice.Status == "issued" {
			found := false
			for _, allocation := range account.Allocations.Invoices {
				if allocation.InvoiceID != invoice.ID {
					continue
				}
				// Use this same snapshot for the selected document and the bridge.
				remaining = allocation.Remaining
				found = true
				break
			}
			if !found {
				s.invoiceRequestError(w, r, store.ErrNotFound)
				return
			}
		}
		view, err = newInvoiceDocumentView(invoice, remaining)
		if err == nil {
			view, err = view.withSettlementLines(account.Lines[invoice.ID])
		}
		view.SettlementAsOf = time.Now()
		data["AccountReconciliation"] = account.InvoiceAccountReconciliation
	} else {
		view, err = s.loadInvoiceDocumentView(r.Context(), invoice)
	}
	if err != nil {
		s.invoiceRequestError(w, r, err)
		return
	}
	data["Doc"], data["Invoice"], data["Neighbor"], data["Year"] = view, invoice, neighbor, year
	data["Shared"], data["Completed"] = token != "", year.Completed()
	if view.Active && view.Remaining.IsPositive() {
		company, err := s.store.GetCompany(r.Context())
		if err != nil {
			s.serverError(w, "invoice payment term", err)
			return
		}
		termNeighbor := neighbor
		if token != "" {
			// Public document parties stay frozen; use only the current payment
			// term, matching the existing reminder policy for historical invoices.
			liveNeighbor, err := s.store.GetNeighbor(r.Context(), neighbor.ID)
			if err != nil {
				s.serverError(w, "invoice payment term", err)
				return
			}
			termNeighbor = liveNeighbor
		}
		term := invoicePaymentTerm(invoice, company, *termNeighbor)
		due := invoice.IssuedOn.AddDate(0, 0, term)
		data["DueOn"], data["DueDays"] = due, calc.DaysBetween(time.Now(), due)
	}
	data["EInvoiceMissing"] = einvoice.MissingFields(invoice)
	data["PortalToken"], data["PortalFeedbackEnabled"] = token, shareID != 0
	data["PortalFeedbackResult"] = feedbackResult(r)
	feedback, err := s.store.ListBelegFeedback(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "invoice feedback", err)
		return
	}
	selectedFeedback := make([]models.BelegFeedback, 0)
	for _, item := range feedback {
		if item.InvoiceID == invoice.ID {
			selectedFeedback = append(selectedFeedback, item)
		}
	}
	data["Feedback"] = selectedFeedback
	if token == "" {
		documents, err := s.store.ListInvoiceDocuments(r.Context(), year.ID, neighbor.ID)
		if err != nil {
			s.serverError(w, "invoice credit history", err)
			return
		}
		data["AttachedCredits"] = attachedInvoiceCredits(invoice, documents)
		if err := s.withYearSelector(r, data, year); err != nil {
			s.serverError(w, "invoice year selector", err)
			return
		}
		data["LastSend"], err = s.store.LastInvoiceBelegSend(r.Context(), year.ID, neighbor.ID, invoice.ID)
		if err != nil {
			s.serverError(w, "invoice send history", err)
			return
		}
		data["MailEnabled"], data["NeighborEmail"] = s.cfg.MailEnabled(), neighbor.Email
		shares, err := s.store.ListBelegShares(r.Context(), neighbor.ID, year.ID)
		if err != nil {
			s.serverError(w, "invoice shares", err)
			return
		}
		selectedShares := make([]models.BelegShare, 0)
		for _, share := range shares {
			if share.InvoiceID != nil && *share.InvoiceID == invoice.ID {
				selectedShares = append(selectedShares, share)
			}
		}
		data["Shares"] = selectedShares
		data["ShareDayOptions"], data["ShareDefaultDays"] = belegShareDayOptions, belegShareDefaultDays
		if c, err := s.cookie(r, shareOnceCookie); err == nil && c.Value != "" && len(c.Value) <= maxBelegShareTokenLen {
			s.setCookie(w, r, &http.Cookie{Name: shareOnceCookie, Value: "", MaxAge: -1})
			_, nID, yID, iID, ok, err := s.store.ResolveInvoiceBelegShareAccess(r.Context(), store.HashToken(c.Value))
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				s.serverError(w, "invoice new share", err)
				return
			}
			if ok && nID == neighbor.ID && yID == year.ID && iID == invoice.ID {
				data["ShareToken"], data["ShareURL"] = c.Value, s.absoluteURL(r, "/s/portal/"+c.Value)
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, "invoice_document", data)
}

func attachedInvoiceCredits(invoice models.Invoice, documents []models.Invoice) []models.Invoice {
	var credits []models.Invoice
	for _, document := range documents {
		if document.Kind == "gutschrift" && document.Status == "issued" &&
			document.BillingYearID == invoice.BillingYearID && document.NeighborID == invoice.NeighborID &&
			document.ReferencesInvoiceID != nil && *document.ReferencesInvoiceID == invoice.ID {
			credits = append(credits, document)
		}
	}
	return credits
}
