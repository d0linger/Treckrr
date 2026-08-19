package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// batchIssueRow is one neighbor's line in the Sammel-Festschreibung preview.
type batchIssueRow struct {
	Neighbor        models.Neighbor
	Bookings        int
	Gross           decimal.Decimal
	AlreadyInvoiced bool
	Missing         []string // mandatory fields still to fill (blocks issuing)
	Issuable        bool
}

// batchIssueRows builds the per-neighbor preview for a year: who would get an
// invoice, who is blocked (missing §11 data) and who already has one. Neighbors
// with no bookings are excluded entirely.
func (s *Server) batchIssueRows(r *http.Request, yearID int64) ([]batchIssueRow, int, error) {
	neighbors, err := s.store.ListYearNeighbors(r.Context(), yearID)
	if err != nil {
		return nil, 0, err
	}
	var rows []batchIssueRow
	issuable := 0
	for _, n := range neighbors {
		cnt, err := s.store.CountEntriesForNeighborYear(r.Context(), yearID, n.ID)
		if err != nil {
			return nil, 0, err
		}
		if cnt == 0 {
			continue // nothing to bill
		}
		row := batchIssueRow{Neighbor: n, Bookings: cnt}
		if _, err := s.store.GetInvoice(r.Context(), yearID, n.ID); err == nil {
			row.AlreadyInvoiced = true
			rows = append(rows, row)
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, 0, err
		}
		content, err := s.store.BuildInvoiceContent(r.Context(), yearID, n.ID)
		if err != nil {
			return nil, 0, err
		}
		row.Gross = content.Gross
		row.Missing = content.MissingMandatory()
		row.Issuable = len(row.Missing) == 0
		if row.Issuable {
			issuable++
		}
		rows = append(rows, row)
	}
	return rows, issuable, nil
}

// handleBatchIssuePreview shows the Sammel-Festschreibung preview: every neighbor
// with bookings, their gross, and whether they'd be issued, blocked or skipped.
func (s *Server) handleBatchIssuePreview(w http.ResponseWriter, r *http.Request) {
	yearID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, issuable, err := s.batchIssueRows(r, yearID)
	if err != nil {
		s.serverError(w, "batch issue: preview", err)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, "batch issue: company", err)
		return
	}
	total := decimal.Zero
	for _, row := range rows {
		if row.Issuable {
			total = total.Add(row.Gross)
		}
	}
	data := s.newPage(w, r, "Sammel-Festschreibung · "+year.Label, "dashboard")
	data["Year"] = year
	data["Rows"] = rows
	data["Issuable"] = issuable
	data["Total"] = total
	data["CompanyOK"] = strings.TrimSpace(company.Name) != ""
	s.render(w, r, "batch_invoice", data)
}

// handleBatchIssueCommit issues invoices for every issuable neighbor in the year.
// Naturally idempotent: it re-checks and skips any neighbor already invoiced, so a
// re-submit issues nothing new. Blocked neighbors are left untouched. Irreversible
// per invoice (§131), so it's reached only via the preview's explicit confirm.
func (s *Server) handleBatchIssueCommit(w http.ResponseWriter, r *http.Request) {
	yearID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if company, err := s.store.GetCompany(r.Context()); err != nil || strings.TrimSpace(company.Name) == "" {
		s.setFlash(w, r, "error", "Bitte zuerst die Betriebsdaten (Absender) ausfüllen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	rows, _, err := s.batchIssueRows(r, yearID)
	if err != nil {
		s.serverError(w, "batch issue: commit", err)
		return
	}
	issued, skipped := 0, 0
	for _, row := range rows {
		if !row.Issuable || row.AlreadyInvoiced {
			skipped++
			continue
		}
		iv, err := s.store.IssueInvoice(r.Context(), yearID, row.Neighbor.ID, year.Year)
		if err != nil {
			// One failure doesn't roll back the others (each invoice is its own
			// festgeschriebenes document); report progress and stop.
			s.audit(r, "invoice_issue_batch", "year", yearID, fmt.Sprintf("Abbruch nach %d Rechnung(en): %s", issued, row.Neighbor.Name))
			s.setFlash(w, r, "error", fmt.Sprintf("%d Rechnung(en) ausgestellt, dann Fehler bei %s. Bitte prüfen.", issued, row.Neighbor.Name))
			redirect(w, r, dashboardURL(yearID))
			return
		}
		s.audit(r, "invoice_issue", "neighbor", row.Neighbor.ID, row.Neighbor.Name+" · Rechnung "+iv.Number+" (Sammel)")
		issued++
	}
	s.audit(r, "invoice_issue_batch", "year", yearID, fmt.Sprintf("%d ausgestellt, %d übersprungen", issued, skipped))
	s.setFlash(w, r, "success", fmt.Sprintf("%d Rechnung(en) festgeschrieben (%d übersprungen).", issued, skipped))
	redirect(w, r, dashboardURL(yearID))
}
