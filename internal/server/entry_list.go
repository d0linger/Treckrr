package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/d0linger/treckrr/internal/store"
)

// ---- Buchungsliste und Sammelaktionen (Ausbaukarte 63/64) ------------------

const entryPageSize = 50

// parseDay reads a yyyy-mm-dd filter value; an empty or malformed one means
// "no bound" rather than today, so a typo widens the list instead of silently
// hiding everything.
func parseDay(v string) time.Time {
	// ParseInLocation, not Parse: the value is a LOCAL calendar day. Against the
	// entries DATE column only Y/M/D travel either way, but the audit filter
	// compares ::timestamptz — UTC midnight would shift every day window by the
	// UTC offset and drop the first hours of each day from the §132 export.
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(v), time.Local)
	if err != nil {
		return time.Time{}
	}
	return d
}

// entryFilterFromQuery builds the filter from the query string, whitelisting
// every value that reaches SQL.
func entryFilterFromQuery(r *http.Request, yearID int64) store.EntryFilter {
	q := r.URL.Query()
	f := store.EntryFilter{
		YearID: yearID,
		From:   parseDay(q.Get("from")),
		To:     parseDay(q.Get("to")),
		Task:   strings.TrimSpace(q.Get("task")),
		Unit:   strings.TrimSpace(q.Get("unit")),
		Limit:  entryPageSize,
	}
	if id, err := strconv.ParseInt(q.Get("neighbor_id"), 10, 64); err == nil && id > 0 {
		f.NeighborID = id
	}
	switch q.Get("voided") {
	case "only", "hide":
		f.Voided = q.Get("voided")
	}
	switch q.Get("sort") {
	case "cost", "neighbor":
		f.Sort = q.Get("sort")
	default:
		f.Sort = "date"
	}
	f.Desc = q.Get("dir") == "desc"
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 {
		f.Offset = (p - 1) * entryPageSize
	}
	return f
}

// handleEntryList renders the year's bookings with filters, sorting and paging.
func (s *Server) handleEntryList(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	f := entryFilterFromQuery(r, year.ID)
	rows, total, sum, err := s.store.FilterEntries(r.Context(), f)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	neighbors, err := s.store.ListYearNeighbors(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	units, err := s.store.EntryUnitsInYear(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	photoCounts, err := s.store.PhotoCounts(r.Context(), year.ID, 0)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	page := f.Offset/entryPageSize + 1
	pages := (total + entryPageSize - 1) / entryPageSize
	data := s.newPage(w, r, "Buchungen", "entries")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Year"] = year
	data["Rows"] = rows
	data["Total"] = total
	data["SumCost"] = sum
	data["Neighbors"] = neighbors
	data["Units"] = units
	data["PhotoCounts"] = photoCounts
	data["Completed"] = year.Completed()
	data["Page"] = page
	data["Pages"] = pages
	data["HasPrev"] = page > 1
	data["HasNext"] = page < pages
	// Query strings for the pager and the sort links, with the current filter
	// preserved — a pager that drops the filter is worse than none.
	data["PrevURL"] = entryListURL(r, year.ID, page-1, "")
	data["NextURL"] = entryListURL(r, year.ID, page+1, "")
	data["SortDateURL"] = entryListURL(r, year.ID, 1, "date")
	data["SortCostURL"] = entryListURL(r, year.ID, 1, "cost")
	data["SortNeighborURL"] = entryListURL(r, year.ID, 1, "neighbor")
	data["Filter"] = map[string]string{
		"from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to"),
		"task": f.Task, "unit": f.Unit, "voided": f.Voided,
		"neighbor_id": r.URL.Query().Get("neighbor_id"),
		"sort":        f.Sort, "dir": r.URL.Query().Get("dir"),
	}
	data["ReturnTo"] = r.URL.RequestURI()
	s.render(w, r, "entries", data)
}

// entryListURL rebuilds the list URL keeping the current filter. With a sort
// key it toggles the direction when that column is already the active one.
func entryListURL(r *http.Request, yearID int64, page int, sort string) string {
	q := url.Values{}
	for _, k := range []string{"from", "to", "task", "unit", "voided", "neighbor_id", "sort", "dir"} {
		if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
			q.Set(k, v)
		}
	}
	q.Set("year", itoa64(yearID))
	if sort != "" {
		if q.Get("sort") == sort && q.Get("dir") != "desc" {
			q.Set("dir", "desc")
		} else {
			q.Del("dir")
		}
		q.Set("sort", sort)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return "/buchungen?" + q.Encode()
}

// formIDs reads the checked ids of a bulk form, capped so one request cannot
// ask the database to touch an unbounded set.
func formIDs(r *http.Request, field string) []int64 {
	raw := r.PostForm[field]
	if len(raw) > 500 {
		raw = raw[:500]
	}
	ids := make([]int64, 0, len(raw))
	for _, v := range raw {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids
}

// handleEntryBulk applies a bulk action to the checked bookings (Ausbaukarte
// 64). The store enforces the year and invoice locks per row, so a partially
// locked selection does the allowed part and says how much that was.
func (s *Server) handleEntryBulk(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	back := "/buchungen?year=" + itoa64(yearID)
	if ret := r.FormValue("return_to"); strings.HasPrefix(ret, "/buchungen") {
		back = ret
	}
	ids := formIDs(r, "entry_id")
	if len(ids) == 0 {
		s.setFlash(w, r, "info", "Keine Buchung ausgewählt.")
		redirect(w, r, back)
		return
	}
	reason := trimmed(r, "reason")
	if s.tooLong(w, r, "Grund", reason, maxNoteLen) {
		redirect(w, r, back)
		return
	}

	var n int
	var err error
	var verb string
	switch r.FormValue("action") {
	case "void":
		n, err = s.store.VoidEntries(r.Context(), ids, true, reason)
		verb = "storniert"
	case "unvoid":
		n, err = s.store.VoidEntries(r.Context(), ids, false, "")
		verb = "wieder aktiviert"
	case "delete":
		n, err = s.store.DeleteEntries(r.Context(), ids)
		verb = "gelöscht"
	default:
		s.badRequest(w, "Unbekannte Aktion.")
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "bulk_"+r.FormValue("action"), "year", yearID,
		fmt.Sprintf("%d von %d Buchung(en) %s%s", n, len(ids), verb, auditReason(reason)))
	if n < len(ids) {
		s.setFlash(w, r, "info", fmt.Sprintf("%d von %d Buchung(en) %s — der Rest ist durch eine festgeschriebene Rechnung oder ein abgeschlossenes Jahr gesperrt.", n, len(ids), verb))
	} else {
		s.setFlash(w, r, "success", fmt.Sprintf("%d Buchung(en) %s.", n, verb))
	}
	redirect(w, r, back)
}

// auditReason appends a reason to an audit detail, or nothing.
func auditReason(reason string) string {
	if reason == "" {
		return ""
	}
	return " · " + reason
}

// ---- Duplizieren für Ledger und Zahlungen (Ausbaukarte 67) -----------------
//
// Bookings had /copy; recurring Verrechnungspositionen and payments had to be
// retyped. Both reuse their existing edit template in "copy" mode: the form is
// prefilled from the source but posts to the ADD route, so nothing is written
// until the operator submits.

// handleLedgerCopy prefills the ledger form from an existing posting.
func (s *Server) handleLedgerCopy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID, neighborID, e, err := s.store.GetLedgerEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), neighborID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	e.Date = time.Now() // a copy is a fresh posting for today
	data := s.newPage(w, r, "Position duplizieren", "dashboard")
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Ledger"] = e
	data["IsCredit"] = e.Amount.IsNegative()
	data["AbsAmount"] = e.Amount.Abs()
	data["Copy"] = true
	s.render(w, r, "ledger_edit", data)
}

// handlePaymentCopy prefills the payment form from an existing payment.
func (s *Server) handlePaymentCopy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p.PaidOn = time.Now()
	// The copy is a NEW payment, so it must not claim the source's invoice link
	// or creation date — AddPayment resolves the current invoice itself.
	p.InvoiceID, p.InvoiceNumber = nil, ""
	data := s.newPage(w, r, "Zahlung duplizieren", "dashboard")
	data["Payment"] = p
	data["NeighborName"] = s.neighborName(r, p.NeighborID)
	data["Back"] = neighborURL(p.NeighborID, p.BillingYearID)
	data["Copy"] = true
	s.render(w, r, "payment_edit", data)
}
