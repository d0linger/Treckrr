package server

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/bankimport"
	"github.com/d0linger/treckrr/internal/store"
)

// paymentImportRow is one parsed bank credit with its match result, for the
// dry-run preview.
type paymentImportRow struct {
	Txn             bankimport.Txn
	Matched         bool
	MatchedBy       string // "Referenz", "IBAN" or "manuell"
	NeighborID      int64
	NeighborName    string
	YearID          int64
	InvoiceID       int64
	InvoiceNumber   string
	AlreadyImported bool
}

func (r paymentImportRow) Importable() bool { return r.Matched && !r.AlreadyImported }

// matchTxns resolves each transaction, in this order: the invoice reference in
// the remittance text, then the payer IBAN against the neighbor master data
// (booking against the neighbor's newest issued invoice), then a manual
// assignment from the preview (keyed by transaction hash, validated against the
// assignable-invoice list so a crafted id cannot book against arbitrary rows).
// Already-imported credits are flagged and never re-booked.
func (s *Server) matchTxns(r *http.Request, txns []bankimport.Txn, assign map[string]int64) ([]paymentImportRow, int, error) {
	var assignable map[int64]store.AssignableInvoice
	if len(assign) > 0 {
		list, err := s.store.ListAssignableInvoices(r.Context())
		if err != nil {
			return nil, 0, err
		}
		assignable = make(map[int64]store.AssignableInvoice, len(list))
		for _, a := range list {
			assignable[a.ID] = a
		}
	}
	rows := make([]paymentImportRow, 0, len(txns))
	importable := 0
	for _, t := range txns {
		row := paymentImportRow{Txn: t}
		if seen, err := s.store.PaymentImportSeen(r.Context(), t.Hash); err != nil {
			return nil, 0, err
		} else {
			row.AlreadyImported = seen
		}
		iv, err := s.store.InvoiceByReferenceText(r.Context(), t.Reference)
		if err != nil {
			return nil, 0, err
		}
		if iv == nil && t.IBAN != "" {
			// Fallback: a known payer account books against that neighbor's
			// newest issued invoice — under neighbors the remittance text is
			// often just "Aushilfe" with no reference at all.
			nid, err := s.store.NeighborIDByIBAN(r.Context(), t.IBAN)
			if err != nil {
				return nil, 0, err
			}
			if nid != 0 {
				if iv, err = s.store.LatestOpenInvoiceForNeighbor(r.Context(), nid); err != nil {
					return nil, 0, err
				}
				if iv != nil {
					row.MatchedBy = "IBAN"
				}
			}
		}
		if iv != nil {
			row.Matched = true
			if row.MatchedBy == "" {
				row.MatchedBy = "Referenz"
			}
			row.NeighborID = iv.NeighborID
			row.YearID = iv.BillingYearID
			row.InvoiceID = iv.ID
			row.InvoiceNumber = iv.Number
			row.NeighborName = s.neighborName(r, iv.NeighborID)
		} else if id, ok := assign[t.Hash]; ok {
			if a, ok := assignable[id]; ok {
				row.Matched = true
				row.MatchedBy = "manuell"
				row.NeighborID = a.NeighborID
				row.YearID = a.YearID
				row.InvoiceID = a.ID
				row.InvoiceNumber = a.Number
				row.NeighborName = a.NeighborName
			}
		}
		if row.Importable() {
			importable++
		}
		rows = append(rows, row)
	}
	return rows, importable, nil
}

// handlePaymentImportForm renders the upload form.
func (s *Server) handlePaymentImportForm(w http.ResponseWriter, r *http.Request) {
	data := s.newPage(w, r, "Zahlungen importieren", "dashboard")
	s.render(w, r, "payment_import", data)
}

// handlePaymentImportPreview parses the uploaded statement and shows which credits
// match an invoice. Nothing is written; the raw content is echoed for commit.
func (s *Server) handlePaymentImportPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.setFlash(w, r, "error", "Bitte eine CSV- oder camt.053-Datei wählen.")
		redirect(w, r, "/payments/import")
		return
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 4<<20))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	txns, perr := bankimport.Parse(raw)
	if perr != nil {
		s.setFlash(w, r, "error", perr.Error())
		redirect(w, r, "/payments/import")
		return
	}
	rows, importable, err := s.matchTxns(r, txns, nil)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Offer manual assignment for whatever stayed unmatched.
	unmatched := false
	for _, row := range rows {
		if !row.Matched && !row.AlreadyImported {
			unmatched = true
			break
		}
	}
	data := s.newPage(w, r, "Zahlungs-Import Vorschau", "dashboard")
	if unmatched {
		assignable, err := s.store.ListAssignableInvoices(r.Context())
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		data["Assignable"] = assignable
	}
	data["Rows"] = rows
	data["Importable"] = importable
	data["Total"] = len(rows)
	data["Raw"] = string(raw)
	s.render(w, r, "payment_import", data)
}

// handlePaymentImportCommit re-parses the echoed statement and books each matched,
// not-yet-imported credit as a payment. RecordPaymentImport dedups so a re-submit
// (or re-uploading the same statement) never double-books.
func (s *Server) handlePaymentImportCommit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	raw := r.FormValue("raw")
	if len(raw) > maxImportPayloadLen {
		s.setFlash(w, r, "error", "Importdaten zu groß.")
		redirect(w, r, "/payments/import")
		return
	}
	txns, perr := bankimport.Parse([]byte(raw))
	if perr != nil {
		s.setFlash(w, r, "error", perr.Error())
		redirect(w, r, "/payments/import")
		return
	}
	// Manual assignments from the preview: assign_<hash> → invoice id.
	assign := make(map[string]int64)
	for key, vals := range r.PostForm {
		if !strings.HasPrefix(key, "assign_") || len(vals) == 0 {
			continue
		}
		if id, err := strconv.ParseInt(vals[0], 10, 64); err == nil && id > 0 {
			assign[strings.TrimPrefix(key, "assign_")] = id
		}
	}
	rows, _, err := s.matchTxns(r, txns, assign)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	booked := 0
	for _, row := range rows {
		if !row.Importable() {
			continue
		}
		note := "Bank-Import"
		if ref := strings.TrimSpace(row.Txn.Reference); ref != "" {
			note += ": " + ref
		}
		// A statement without a parseable date carries the zero time; book it as
		// received today. The de-dup hash is unaffected (it never uses time.Now()).
		paidOn := row.Txn.Date
		if paidOn.IsZero() {
			paidOn = time.Now()
		}
		// Atomic: hash + payment are booked together, so a failure never leaves the
		// credit marked-imported-but-unbooked (which would skip it forever).
		fresh, err := s.store.ImportPayment(r.Context(), row.Txn.Hash, row.YearID, row.NeighborID, row.InvoiceID, row.Txn.Amount, paidOn, note)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if !fresh {
			continue // a concurrent/earlier import already booked it
		}
		s.audit(r, "payment_import", "neighbor", row.NeighborID, row.NeighborName+" · "+row.Txn.Amount.StringFixed(2)+" € · Rechnung "+row.InvoiceNumber+" ("+row.MatchedBy+")")
		booked++
	}
	s.setFlash(w, r, "success", itoa(booked)+" Zahlung(en) importiert und zugeordnet.")
	redirect(w, r, "/payments/import")
}
