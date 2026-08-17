package server

import (
	"io"
	"net/http"
	"strings"

	"github.com/d0linger/treckrr/internal/bankimport"
)

// paymentImportRow is one parsed bank credit with its match result, for the
// dry-run preview.
type paymentImportRow struct {
	Txn             bankimport.Txn
	Matched         bool
	NeighborID      int64
	NeighborName    string
	YearID          int64
	InvoiceNumber   string
	AlreadyImported bool
}

func (r paymentImportRow) Importable() bool { return r.Matched && !r.AlreadyImported }

// matchTxns resolves each transaction against an issued invoice (by reference) and
// flags already-imported ones.
func (s *Server) matchTxns(r *http.Request, txns []bankimport.Txn) ([]paymentImportRow, int, error) {
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
		if iv != nil {
			row.Matched = true
			row.NeighborID = iv.NeighborID
			row.YearID = iv.BillingYearID
			row.InvoiceNumber = iv.Number
			row.NeighborName = s.neighborName(r, iv.NeighborID)
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
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
	rows, importable, err := s.matchTxns(r, txns)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Zahlungs-Import Vorschau", "dashboard")
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	txns, perr := bankimport.Parse([]byte(r.FormValue("raw")))
	if perr != nil {
		s.setFlash(w, r, "error", perr.Error())
		redirect(w, r, "/payments/import")
		return
	}
	rows, _, err := s.matchTxns(r, txns)
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
		// Atomic: hash + payment are booked together, so a failure never leaves the
		// credit marked-imported-but-unbooked (which would skip it forever).
		fresh, err := s.store.ImportPayment(r.Context(), row.Txn.Hash, row.YearID, row.NeighborID, row.Txn.Amount, row.Txn.Date, note)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if !fresh {
			continue // a concurrent/earlier import already booked it
		}
		s.audit(r, "payment_import", "neighbor", row.NeighborID, row.NeighborName+" · "+row.Txn.Amount.StringFixed(2)+" € · Rechnung "+row.InvoiceNumber)
		booked++
	}
	s.setFlash(w, r, "success", itoa(booked)+" Zahlung(en) importiert und zugeordnet.")
	redirect(w, r, "/payments/import")
}
