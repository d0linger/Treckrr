package server

import (
	"crypto/sha256"
	"errors"
	"fmt"
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
	batches, err := s.store.ListPaymentImportBatches(r.Context(), 20)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Zahlungen importieren", "payimport")
	data["Batches"] = batches
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
	data := s.newPage(w, r, "Zahlungs-Import Vorschau", "payimport")
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
	batchRows := make([]store.PaymentImportRowInput, 0, len(rows))
	for i, row := range rows {
		status, reason := store.PaymentImportPending, ""
		switch {
		case row.AlreadyImported:
			status, reason = store.PaymentImportDuplicate, "Bereits in einem früheren Import verbucht"
		case !row.Matched:
			status, reason = store.PaymentImportUnmatched, "Keine Rechnung zugeordnet"
		}
		batchRows = append(batchRows, store.PaymentImportRowInput{
			RowNo: i + 1, TransactionHash: row.Txn.Hash, TransactionDate: row.Txn.Date,
			Amount: row.Txn.Amount, Reference: row.Txn.Reference, PayerName: row.Txn.Name,
			PayerIBAN: row.Txn.IBAN, MatchMethod: row.MatchedBy, Status: status, Reason: reason,
			YearID: row.YearID, NeighborID: row.NeighborID, InvoiceID: row.InvoiceID,
		})
	}
	sourceHash := sha256.Sum256(raw)
	batchID, batchRowIDs, err := s.store.CreatePaymentImportBatch(
		r.Context(), s.currentUserID(r), fmt.Sprintf("%x", sourceHash), batchRows,
	)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	booked := 0
	var skipped []paymentImportSkip
	for i, row := range rows {
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
		fresh, err := s.store.ImportPayment(r.Context(), batchRowIDs[i], row.Txn.Hash, row.YearID, row.NeighborID, row.InvoiceID, row.Txn.Amount, paidOn, note)
		if err != nil {
			reason, business := paymentImportSkipReason(err)
			if !business {
				// Rows booked so far are committed and audited (ImportPayment
				// writes its audit row in the same transaction).
				_ = s.store.FinishPaymentImportBatch(r.Context(), batchID, true)
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
			if err := s.store.MarkPaymentImportRowSkipped(r.Context(), batchRowIDs[i], reason); err != nil {
				_ = s.store.FinishPaymentImportBatch(r.Context(), batchID, true)
				s.serverError(w, r.URL.Path, err)
				return
			}
			continue
		}
		if !fresh {
			continue // a concurrent/earlier import already booked it
		}
		booked++
	}
	if err := s.store.FinishPaymentImportBatch(r.Context(), batchID, false); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	kind := "success"
	if len(skipped) > 0 {
		kind = "error"
	}
	s.setFlash(w, r, kind, paymentImportSummary(booked, skipped))
	redirect(w, r, "/payments/import/batches/"+itoa64(batchID))
}

// handlePaymentImportBatch renders the durable outcome of one statement run.
func (s *Server) handlePaymentImportBatch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	batch, rows, err := s.store.GetPaymentImportBatch(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Zahlungsimport #"+itoa64(id), "payimport")
	data["Batch"], data["Rows"] = batch, rows
	s.render(w, r, "payment_import_batch", data)
}

// handlePaymentImportReport exports the exact stored outcomes, not a newly
// parsed approximation of the source file.
func (s *Server) handlePaymentImportReport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	batch, rows, err := s.store.GetPaymentImportBatch(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	cw, finish := csvDownload(w, r, "zahlungsimport-"+itoa64(id)+".csv")
	defer finish()
	_ = cw.Write([]string{"Import", "Quell-Fingerabdruck", "Position", "Datum", "Betrag (EUR)", "Status", "Grund", "Zuordnung", "Verwendungszweck", "Zahler", "IBAN", "Zahlungs-ID", "Korrektur-ID"})
	for _, row := range rows {
		date := ""
		if row.TransactionDate != nil {
			date = row.TransactionDate.Format("2006-01-02")
		}
		_ = cw.Write([]string{
			strconv.FormatInt(batch.ID, 10), batch.SourceSHA256, strconv.Itoa(row.RowNo), date,
			deDecimal(row.Amount), row.StatusLabel(), csvSafe(row.Reason), csvSafe(row.MatchMethod),
			csvSafe(row.Reference), csvSafe(row.PayerName), csvSafe(row.PayerIBAN),
			strconv.FormatInt(row.PaymentID, 10), strconv.FormatInt(row.ReversalPaymentID, 10),
		})
	}
}

// handlePaymentImportReverse corrects one imported credit through an equal
// counter-entry. The original payment and import row remain unchanged evidence.
func (s *Server) handlePaymentImportReverse(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	_, batchID, err := s.store.ReverseImportedPayment(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrImportAlreadyReversed):
		s.setFlash(w, r, "info", "Diese Importzahlung wurde bereits durch eine Gegenbuchung korrigiert.")
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r.URL.Path, err)
		return
	default:
		s.setFlash(w, r, "success", "Korrektur als Gegenbuchung erfasst. Die ursprüngliche Zahlung bleibt im Journal erhalten.")
	}
	redirect(w, r, "/payments/import/batches/"+itoa64(batchID))
}
