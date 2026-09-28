package server

import (
	"errors"
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

// msgImportTooLarge is the flash for an import file over maxImportPayloadLen.
const msgImportTooLarge = "Die Datei ist zu groß — höchstens 4 MB sind möglich. Bitte den Export auf einen kürzeren Zeitraum beschränken."

// msgImportExpired is the flash for a commit whose server-side upload is gone
// (expired, already purged, or never made on this account).
const msgImportExpired = "Die Vorschau ist abgelaufen oder ungültig — bitte die Datei erneut hochladen und prüfen."

// readImportFile reads an uploaded import file, reporting (nil, false, nil)
// when it exceeds maxImportPayloadLen. The old io.LimitReader silently
// truncated such a file and parsed whatever prefix fit.
func readImportFile(file io.Reader) ([]byte, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(file, maxImportPayloadLen+1))
	if err != nil {
		return nil, false, err
	}
	if len(raw) > maxImportPayloadLen {
		return nil, false, nil
	}
	return raw, true, nil
}

// currentUserID is the id of the signed-in user, 0 without one.
func (s *Server) currentUserID(r *http.Request) int64 {
	if u := s.currentUser(r); u != nil {
		return u.ID
	}
	return 0
}

// handlePaymentImportPreview parses the uploaded statement and shows which credits
// match an invoice. No payment is written; the file itself is parked server-side
// (store.SaveImportUpload) and the commit names it by token. Echoing the raw
// file through a hidden field made a medium-sized statement exceed the body cap
// on its way back and fail as a bogus CSRF error.
func (s *Server) handlePaymentImportPreview(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(maxImportPayloadLen); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			s.setFlash(w, r, "error", msgImportTooLarge)
			redirect(w, r, "/payments/import")
			return
		}
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	file, _, err := r.FormFile("file")
	if err != nil {
		s.setFlash(w, r, "error", "Bitte eine CSV- oder camt.053-Datei wählen.")
		redirect(w, r, "/payments/import")
		return
	}
	defer file.Close()
	raw, fits, err := readImportFile(file)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !fits {
		s.setFlash(w, r, "error", msgImportTooLarge)
		redirect(w, r, "/payments/import")
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
	token, err := s.store.SaveImportUpload(r.Context(), store.ImportUploadPayment, s.currentUserID(r), 0, raw)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Rows"] = rows
	data["Importable"] = importable
	data["Total"] = len(rows)
	data["UploadToken"] = token
	s.render(w, r, "payment_import", data)
}

// paymentImportSkip is a credit the commit could not book, with the reason.
type paymentImportSkip struct {
	Label  string // invoice number, or amount when there is none
	Reason string
}

// paymentImportSkipReason classifies a per-credit ImportPayment failure. A
// business refusal (erased account, invoice no longer issued, neighbor no
// longer in the year) concerns that credit alone: it is reported and the rest
// of the statement is still booked. ok=false means an infrastructure error.
func paymentImportSkipReason(err error) (string, bool) {
	switch {
	case errors.Is(err, store.ErrNeighborAnonymized):
		return "Nachbar anonymisiert", true
	case errors.Is(err, store.ErrNotFound):
		return "Rechnung oder Nachbar nicht mehr buchbar", true
	case errors.Is(err, store.ErrYearCompleted):
		return "Abrechnungsjahr abgeschlossen", true
	case errors.Is(err, store.ErrInvoiceLocked):
		return "Rechnung gesperrt", true
	}
	return "", false
}

// paymentImportSummary is the commit's flash text: booked count first, then
// every skipped credit with its reason (the first few by name).
func paymentImportSummary(booked int, skipped []paymentImportSkip) string {
	msg := itoa(booked) + " Zahlung(en) importiert und zugeordnet."
	if len(skipped) == 0 {
		return msg
	}
	const named = 5
	parts := make([]string, 0, named)
	for i, sk := range skipped {
		if i == named {
			parts = append(parts, "…")
			break
		}
		parts = append(parts, sk.Label+" ("+sk.Reason+")")
	}
	return msg + " " + itoa(len(skipped)) + " übersprungen: " + strings.Join(parts, ", ") + "."
}

// handlePaymentImportCommit re-parses the statement parked by the preview and
// books each matched, not-yet-imported credit as a payment. ImportPayment dedups
// by hash, so a re-submit (or re-uploading the same statement) never
// double-books. A credit the store refuses for a business reason is skipped and
// named in the summary; only an infrastructure error aborts with a 500.
func (s *Server) handlePaymentImportCommit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	token := trimmed(r, "upload_token")
	if token == "" || len(token) > maxNameLen {
		s.setFlash(w, r, "error", msgImportExpired)
		redirect(w, r, "/payments/import")
		return
	}
	raw, err := s.store.LoadImportUpload(r.Context(), token, store.ImportUploadPayment, s.currentUserID(r), 0)
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", msgImportExpired)
		redirect(w, r, "/payments/import")
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	txns, perr := bankimport.Parse(raw)
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
	var skipped []paymentImportSkip
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
			reason, business := paymentImportSkipReason(err)
			if !business {
				// Rows booked so far are committed and audited (ImportPayment
				// writes its audit row in the same transaction).
				s.serverError(w, r.URL.Path, err)
				return
			}
			// The transaction rolled back, hash included: once the cause is
			// fixed, a re-import books this credit.
			label := row.InvoiceNumber
			if label == "" {
				label = deDecimal(row.Txn.Amount) + " €"
			}
			skipped = append(skipped, paymentImportSkip{Label: label, Reason: reason})
			continue
		}
		if !fresh {
			continue // a concurrent/earlier import already booked it
		}
		booked++
	}
	kind := "success"
	if len(skipped) > 0 {
		kind = "error"
	}
	s.setFlash(w, r, kind, paymentImportSummary(booked, skipped))
	redirect(w, r, "/payments/import")
}
