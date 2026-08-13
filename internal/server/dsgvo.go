package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/models"
)

// The DSGVO/GDPR Art. 15 (right of access) & Art. 20 (portability) export: a
// machine-readable JSON document containing every piece of personal data Treckrr
// holds about one neighbor — their master record plus, per billing year, all
// bookings (including voided ones, for completeness) and all invoice documents.

type dsgvoExport struct {
	ExportedAt   time.Time    `json:"exported_at"`
	Notice       string       `json:"notice"`
	Subject      dsgvoSubject `json:"subject"`
	BillingYears []dsgvoYear  `json:"billing_years"`
}

type dsgvoSubject struct {
	ID       int64     `json:"id"`
	Name     string    `json:"name"`
	Address  string    `json:"address,omitempty"`
	TaxID    string    `json:"tax_id,omitempty"`
	Note     string    `json:"note,omitempty"`
	Archived bool      `json:"archived"`
	Created  time.Time `json:"created"`
}

type dsgvoYear struct {
	Year     int            `json:"year"`
	Entries  []dsgvoEntry   `json:"entries"`
	Invoices []dsgvoInvoice `json:"invoices"`
}

type dsgvoEntry struct {
	Date       time.Time       `json:"date"`
	Task       string          `json:"task"`
	Tractor    string          `json:"tractor,omitempty"`
	Load       string          `json:"load,omitempty"`
	Machines   string          `json:"machines,omitempty"`
	Unit       string          `json:"unit"`
	Quantity   decimal.Decimal `json:"quantity"`
	UnitPrice  decimal.Decimal `json:"unit_price"`
	Cost       decimal.Decimal `json:"cost"`
	Note       string          `json:"note,omitempty"`
	Voided     bool            `json:"voided"`
	VoidReason string          `json:"void_reason,omitempty"`
}

type dsgvoInvoice struct {
	Number   string                 `json:"number"`
	Kind     string                 `json:"kind"`
	Status   string                 `json:"status"`
	IssuedOn time.Time              `json:"issued_on"`
	Content  *models.InvoiceContent `json:"content,omitempty"`
}

func dsgvoSubjectFromNeighbor(n *models.Neighbor) dsgvoSubject {
	return dsgvoSubject{
		ID: n.ID, Name: n.Name, Address: n.Address, TaxID: n.TaxID,
		Note: n.Note, Archived: n.Archived, Created: n.Created,
	}
}

func dsgvoEntryFrom(e models.Entry) dsgvoEntry {
	return dsgvoEntry{
		Date: e.Date, Task: e.TaskLabel, Tractor: e.TractorLabel, Load: e.LoadLabel,
		Machines: e.MachineLabels, Unit: e.Unit, Quantity: e.Quantity,
		UnitPrice: e.UnitPrice, Cost: e.Cost, Note: e.Note,
		Voided: e.Voided, VoidReason: e.VoidReason,
	}
}

func dsgvoInvoiceFrom(iv models.Invoice) dsgvoInvoice {
	return dsgvoInvoice{
		Number: iv.Number, Kind: iv.Kind, Status: iv.Status,
		IssuedOn: iv.IssuedOn, Content: iv.Content,
	}
}

// handleNeighborDataExport streams the DSGVO Art. 15 data-subject export for one
// neighbor as an indented JSON attachment. The export itself is audited, since a
// data-access request is a processing activity worth recording.
func (s *Server) handleNeighborDataExport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	n, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	out := dsgvoExport{
		ExportedAt: time.Now(),
		Notice:     "DSGVO Art. 15/20 Datenauskunft — alle zu dieser Person gespeicherten Daten.",
		Subject:    dsgvoSubjectFromNeighbor(n),
	}
	for _, y := range years {
		entries, err := s.store.ListEntries(r.Context(), n.ID, y.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		invoices, err := s.store.ListInvoiceDocuments(r.Context(), y.ID, n.ID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if len(entries) == 0 && len(invoices) == 0 {
			continue // a year with no data for this person adds nothing.
		}
		dy := dsgvoYear{Year: y.Year}
		for _, e := range entries {
			dy.Entries = append(dy.Entries, dsgvoEntryFrom(e))
		}
		for _, iv := range invoices {
			dy.Invoices = append(dy.Invoices, dsgvoInvoiceFrom(iv))
		}
		out.BillingYears = append(out.BillingYears, dy)
	}

	s.audit(r, "export", "neighbor", n.ID, "DSGVO-Auskunft")

	filename := "treckrr_auskunft_" + sanitizeFilename(n.Name) + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		s.serverError(w, r.URL.Path, err)
	}
}
