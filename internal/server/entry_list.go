package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
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
	if p, err := strconv.Atoi(q.Get("page")); err == nil && p > 1 && p <= int(^uint(0)>>1)/entryPageSize {
		f.Offset = (p - 1) * entryPageSize
	}
	return f
}

// bookingFilterFromQuery adds allowlisted direction/type filters to the legacy
// entry filter; ordinary links remain valid and default to both directions.
func bookingFilterFromQuery(r *http.Request, yearID int64) store.BookingFilter {
	f := store.BookingFilter{EntryFilter: entryFilterFromQuery(r, yearID)}
	switch direction := r.URL.Query().Get("direction"); direction {
	case "in", "out":
		f.Direction = direction
	}
	switch kind := r.URL.Query().Get("kind"); kind {
	case "equipment", "labor", "quantity", "fixed", "manual", "transfer":
		f.Kind = kind
	}
	return f
}

// handleEntryList renders the year's bookings with filters, sorting and paging.
func (s *Server) handleEntryList(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	f := bookingFilterFromQuery(r, year.ID)
	if r.URL.Query().Get("export") == "csv" {
		s.handleBookingListExport(w, r, year, f)
		return
	}
	rows, total, sum, err := s.store.FilterBookings(r.Context(), f)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	neighbors, err := s.store.ListYearNeighbors(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	units, err := s.store.BookingUnitsInYear(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Only the rows on THIS page — aggregating the whole year hashed every
	// photo-bearing booking per pager click to decorate 50 rows.
	pageIDs := make([]int64, 0, len(rows))
	ledgerIDs := make([]int64, 0, len(rows))
	for _, e := range rows {
		if !e.IsLedger() {
			pageIDs = append(pageIDs, e.ID)
		} else {
			ledgerIDs = append(ledgerIDs, e.ID)
		}
	}
	photoCounts, err := s.store.PhotoCountsForEntries(r.Context(), pageIDs)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	ledgerPhotoCounts, err := s.store.LedgerPhotoCounts(r.Context(), ledgerIDs)
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
	data["LedgerPhotoCounts"] = ledgerPhotoCounts
	data["HasEntryRows"] = len(pageIDs) > 0
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
	data["ExportURL"] = entryListURL(r, year.ID, 1, "") + "&export=csv"
	data["Filter"] = map[string]string{
		"from": r.URL.Query().Get("from"), "to": r.URL.Query().Get("to"),
		"task": f.Task, "unit": f.Unit, "voided": f.Voided,
		"neighbor_id": r.URL.Query().Get("neighbor_id"),
		"sort":        f.Sort, "dir": r.URL.Query().Get("dir"),
		"direction": f.Direction, "kind": f.Kind,
	}
	data["ReturnTo"] = r.URL.RequestURI()
	s.render(w, r, "entries", data)
}

// entryListURL rebuilds the list URL keeping the current filter. With a sort
// key it toggles the direction when that column is already the active one.
func entryListURL(r *http.Request, yearID int64, page int, sort string) string {
	q := url.Values{}
	for _, k := range []string{"from", "to", "task", "unit", "voided", "neighbor_id", "sort", "dir", "direction", "kind"} {
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

// handleBookingListExport exports exactly the current filter across both data
// sources, with signed amounts, explicit status, and no UI pagination limit.
func (s *Server) handleBookingListExport(w http.ResponseWriter, r *http.Request, year *models.BillingYear, f store.BookingFilter) {
	rows, err := s.store.ExportBookings(r.Context(), f)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	cw, finish := csvDownload(w, r, fmt.Sprintf("treckrr_buchungen_%d.csv", year.Year))
	defer finish()
	if err := cw.Write([]string{"Quelle", "ID", "Nachbar", "Datum", "Richtung", "Art", "Tätigkeit",
		"Einheit", "Menge", "Satz/Einheit (€)", "Betrag (€)", "Status", "Details",
		"Partnergerät", "Personen", "Mannstunden je Person", "Personensätze (€/h)"}); err != nil {
		return
	}
	total := decimal.Zero
	for _, row := range rows {
		status, detail := "aktiv", row.Note
		var partner, person, personHours, personRate string
		if row.Voided {
			status = "storniert"
		} else {
			total = total.Add(row.Cost)
		}
		if row.Booking != nil {
			detail = row.Booking.Summary()
			partner = row.Booking.PartnerLabel
			var names, hours, rates []string
			for _, bookingPerson := range row.Booking.BookingPeople() {
				state := ""
				if bookingPerson.Voided {
					state = " (storniert)"
				}
				names = append(names, bookingPerson.Name+state)
				hours = append(hours, strings.Replace(bookingPerson.Hours.String(), ".", ",", 1))
				rates = append(rates, strings.Replace(bookingPerson.Rate.String(), ".", ",", 1))
			}
			person, personHours, personRate = strings.Join(names, " | "), strings.Join(hours, " | "), strings.Join(rates, " | ")
		}
		if err := cw.Write([]string{row.Source, itoa64(row.ID), csvSafe(row.NeighborName), web.Date(row.Date),
			row.DirectionLabel(), row.KindLabel(), csvSafe(row.TaskLabel), csvSafe(row.Unit),
			strings.Replace(row.Quantity.String(), ".", ",", 1), strings.Replace(row.UnitPrice.String(), ".", ",", 1),
			deDecimal(row.Cost), status, csvSafe(detail), csvSafe(partner), csvSafe(person), personHours, personRate}); err != nil {
			return
		}
	}
	if err := cw.Write([]string{"", "", "", "", "", "", "", "", "", "Summe ohne Storno", deDecimal(total), "", "", "", "", "", ""}); err != nil {
		return
	}
}

// formIDs reads the checked ids of a bulk form, capped so one request cannot
// ask the database to touch an unbounded set.
// formIDs parses a repeated id field. ok=false means the list was over the
// cap — the caller must REFUSE, not proceed: truncating silently made a bulk
// delete of 700 bookings report "500 gelöscht" while 200 stood untouched, and
// formInt64List's contract says exactly this (the copy here had drifted).
func formIDs(r *http.Request, field string) ([]int64, bool) {
	raw := r.PostForm[field]
	if len(raw) > maxFormListLen {
		return nil, false
	}
	ids := make([]int64, 0, len(raw))
	for _, v := range raw {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	return ids, true
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
	if ret := r.FormValue("return_to"); isSafeBuchungenReturnPath(ret) {
		back = ret
	}
	ids, ok := formBookingRefs(r)
	if !ok {
		s.setFlash(w, r, "error", fmt.Sprintf("Zu viele Buchungen auf einmal ausgewählt (höchstens %d).", maxFormListLen))
		redirect(w, r, "/buchungen")
		return
	}
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
		n, err = s.store.VoidBookings(r.Context(), ids, true, reason)
		verb = "storniert"
	case "unvoid":
		n, err = s.store.VoidBookings(r.Context(), ids, false, "")
		verb = "wieder aktiviert"
	case "delete":
		entryIDs := []int64{}
		for _, ref := range ids {
			if ref.Source == "entry" {
				entryIDs = append(entryIDs, ref.ID)
			}
		}
		n, err = s.store.DeleteEntries(r.Context(), entryIDs)
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
		s.setFlash(w, r, "info", fmt.Sprintf("%d von %d Buchung(en) %s — unveränderte oder gesperrte Buchungen und Jahresüberträge wurden übersprungen. Gegenleistungen werden in Sammelaktionen nur storniert, nicht endgültig gelöscht.", n, len(ids), verb))
	} else {
		s.setFlash(w, r, "success", fmt.Sprintf("%d Buchung(en) %s.", n, verb))
	}
	redirect(w, r, back)
}

// isSafeBuchungenReturnPath allows only the booking list and its filters/anchor.
// Reject noncanonical paths rather than returning untrusted traversal segments.
func isSafeBuchungenReturnPath(ret string) bool {
	if strings.Contains(ret, "\\") {
		return false
	}
	u, err := url.Parse(ret)
	if err != nil || u.IsAbs() || u.Host != "" {
		return false
	}
	return u.Path == "/buchungen" && u.RawPath == ""
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
		s.notFound(w, r)
		return
	}
	yearID, neighborID, e, err := s.store.GetLedgerEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), neighborID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	e.Date = time.Now() // a copy is a fresh posting for today
	data := s.newPage(w, r, "Position duplizieren", "dashboard")
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Ledger"] = e
	data["IsCredit"] = e.Amount.IsNegative()
	data["AbsAmount"] = e.Amount.Abs()
	if e.Booking != nil {
		data["BookingKind"] = e.Booking.Kind
		data["BookingDirection"] = "out"
		if e.Amount.IsNegative() {
			data["BookingDirection"] = "in"
		}
	}
	data["Copy"] = true
	if err := s.setLedgerBookingForm(r, data, &e, year, neighborID, true); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.render(w, r, "ledger_edit", data)
}

// handlePaymentCopy prefills the payment form from an existing payment.
func (s *Server) handlePaymentCopy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	p, err := s.store.GetPayment(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
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
