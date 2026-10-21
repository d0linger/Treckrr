package server

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// documentKindLabels names the invoice-family documents in German.
var documentKindLabels = map[string]string{"invoice": "Rechnung", "storno": "Storno", "gutschrift": "Gutschrift", "anzahlung": "Abschlag"}

// handleAnnualReport renders the year's report PDF: totals, accounts per
// neighbor, services, machine utilization and margin, and the invoice
// journal. It reuses the figures of the statistics and the journal, so the
// document and the pages never disagree.
func (s *Server) handleAnnualReport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, "annual report: year", err)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, "annual report: company", err)
		return
	}
	entries, err := s.store.ListEntriesByYear(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "annual report: entries", err)
		return
	}
	summaries, err := s.store.YearNeighborSummaries(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "annual report: accounts", err)
		return
	}
	results, err := s.store.YearNeighborResults(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "annual report: results", err)
		return
	}
	paid, open, credit, err := s.store.YearPaymentTotals(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "annual report: payments", err)
		return
	}
	machines, err := s.store.MachineUsageForYear(r.Context(), year.ID, time.Time{}, time.Time{})
	if err != nil {
		s.serverError(w, "annual report: machines", err)
		return
	}
	journal, err := s.store.ListInvoiceJournal(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "annual report: journal", err)
		return
	}

	report := pdf.AnnualReport{
		IssuerName: company.Name, IssuerAddress: company.Address,
		Year: year.Year, Completed: year.Completed(), Generated: time.Now(),
		Paid: paid, Open: open, Credit: credit,
	}
	tasks := map[string]*pdf.AnnualTask{}
	for _, e := range entries {
		if e.Voided {
			continue
		}
		report.Bookings = report.Bookings.Add(e.Cost)
		report.Hours = report.Hours.Add(e.Hours)
		t := tasks[e.TaskLabel]
		if t == nil {
			t = &pdf.AnnualTask{Label: e.TaskLabel}
			tasks[e.TaskLabel] = t
		}
		t.Count++
		t.Hours = t.Hours.Add(e.Hours)
		t.Cost = t.Cost.Add(e.Cost)
	}
	for _, t := range tasks {
		report.Tasks = append(report.Tasks, *t)
	}
	sort.Slice(report.Tasks, func(i, j int) bool {
		if !report.Tasks[i].Cost.Equal(report.Tasks[j].Cost) {
			return report.Tasks[i].Cost.GreaterThan(report.Tasks[j].Cost)
		}
		return report.Tasks[i].Label < report.Tasks[j].Label
	})
	for _, res := range results {
		report.Ledger = report.Ledger.Add(res.Ledger)
	}
	for _, n := range summaries {
		report.Neighbors = append(report.Neighbors, pdf.AnnualNeighbor{
			Name: n.Name, Entries: n.Entries, Hours: n.Hours, Owed: n.Cost, Paid: n.PaidAmount, Remaining: n.Remaining,
		})
	}
	for _, m := range machines {
		report.Machines = append(report.Machines, pdf.AnnualMachine{
			Name: m.Name, Hours: m.Hours, Revenue: m.Revenue, Margin: m.Margin, HasMargin: m.HasMargin(),
		})
		if m.HasMargin() {
			report.HasMargin = true
			report.MarginTotal = report.MarginTotal.Add(m.Margin)
		}
	}
	for _, j := range journal {
		status := ""
		if j.Status == "canceled" {
			status = "storniert"
		}
		report.Invoices = append(report.Invoices, pdf.AnnualInvoice{
			Number: j.Number, Kind: orDefault(documentKindLabels[j.Kind], j.Kind), Neighbor: j.NeighborName, Status: status,
			IssuedOn: j.IssuedOn, Net: j.Net, VAT: j.VATAmount, Gross: j.Gross,
		})
		if j.CountsForRevenue() {
			report.InvoiceNet = report.InvoiceNet.Add(j.Net)
			report.InvoiceVAT = report.InvoiceVAT.Add(j.VATAmount)
			report.InvoiceGross = report.InvoiceGross.Add(j.Gross)
		}
	}
	report.Bookings = report.Bookings.Round(2)
	report.Ledger = report.Ledger.Round(2)

	blob, err := pdf.RenderAnnualReport(report)
	if err != nil {
		s.serverError(w, "annual report: render", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="Jahresbericht_`+strconv.Itoa(year.Year)+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}
