package server

import (
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/d0linger/treckrr/internal/bankimport"
	"github.com/d0linger/treckrr/internal/store"
)

// bankImportNote formats an imported payment note and bounds untrusted
// remittance text to the same rune limit as manually entered payment notes.
func bankImportNote(reference string) string {
	note := "Bank-Import"
	if ref := strings.TrimSpace(reference); ref != "" {
		note += ": " + ref
	}
	if utf8.RuneCountInString(note) <= maxNoteLen {
		return note
	}
	return string([]rune(note)[:maxNoteLen])
}

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
	// Three set queries for the WHOLE statement — the per-transaction path (an
	// EXISTS, a regex scan, an unindexable IBAN scan and a name SELECT per
	// credit) cost a 400-line statement ~1600 round trips per preview and the
	// same again on commit. Matching itself happens here in Go.
	hashes := make([]string, 0, len(txns))
	for _, t := range txns {
		hashes = append(hashes, t.Hash)
	}
	seen, err := s.store.SeenPaymentHashes(r.Context(), hashes)
	if err != nil {
		return nil, 0, err
	}
	targets, err := s.store.IssuedInvoiceTargets(r.Context())
	if err != nil {
		return nil, 0, err
	}
	ibans, err := s.store.NeighborIBANMap(r.Context())
	if err != nil {
		return nil, 0, err
	}
	// Reference matching mirrors InvoiceByReferenceText: the reference must
	// appear in the remittance text on non-alphanumeric boundaries, the longest
	// reference wins (2026-0001 must not lose to a neighbor whose reference is
	// its prefix). Newest issued invoice per neighbor doubles as the IBAN
	// fallback target, exactly as LatestOpenInvoiceForNeighbor picked it.
	type refPattern struct {
		re *regexp.Regexp
		t  store.InvoiceRefTarget
	}
	patterns := make([]refPattern, 0, len(targets))
	newest := map[int64]store.InvoiceRefTarget{} // neighbor id → newest issued invoice
	for _, t := range targets {
		if ref := strings.TrimSpace(t.PaymentReference); ref != "" {
			re, err := regexp.Compile(`(^|[^0-9A-Za-z])` + regexp.QuoteMeta(ref) + `([^0-9A-Za-z]|$)`)
			if err == nil {
				patterns = append(patterns, refPattern{re: re, t: t})
			}
		}
		if cur, ok := newest[t.NeighborID]; !ok || t.ID > cur.ID {
			newest[t.NeighborID] = t
		}
	}
	sort.SliceStable(patterns, func(i, j int) bool {
		return len(patterns[i].t.PaymentReference) > len(patterns[j].t.PaymentReference)
	})

	rows := make([]paymentImportRow, 0, len(txns))
	importable := 0
	for _, t := range txns {
		row := paymentImportRow{Txn: t, AlreadyImported: seen[t.Hash]}
		var match *store.InvoiceRefTarget
		for i := range patterns {
			if patterns[i].re.MatchString(t.Reference) {
				match = &patterns[i].t
				break
			}
		}
		if match == nil && t.IBAN != "" {
			// Fallback: a known payer account books against that neighbor's
			// newest issued invoice — under neighbors the remittance text is
			// often just "Aushilfe" with no reference at all.
			norm := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(t.IBAN), " ", ""))
			if nid := ibans[norm]; nid != 0 {
				if nv, ok := newest[nid]; ok {
					match = &nv
					row.MatchedBy = "IBAN"
				}
			}
		}
		if match != nil {
			row.Matched = true
			if row.MatchedBy == "" {
				row.MatchedBy = "Referenz"
			}
			row.NeighborID = match.NeighborID
			row.YearID = match.YearID
			row.InvoiceID = match.ID
			row.InvoiceNumber = match.Number
			row.NeighborName = match.NeighborName
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
		note := bankImportNote(row.Txn.Reference)
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
		booked++
	}
	s.setFlash(w, r, "success", itoa(booked)+" Zahlung(en) importiert und zugeordnet.")
	redirect(w, r, "/payments/import")
}
