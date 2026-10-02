package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/money"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

// handleNeighborDetail assembles the selected year's bookings, ledger, payments,
// and capture forms for one neighbor. Stale-price markers are best-effort and do
// not prevent the account page from rendering when their lookups fail.
func (s *Server) handleNeighborDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	base := year.Base

	settlements := s.settlements()
	account, err := settlements.Load(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	entries := account.Entries
	ledger := account.Ledger

	// Bookings whose stored price no longer matches the current basis (the basis
	// was edited after they were booked). Marked in the table; offered for
	// recalculation. Best-effort — a failure just omits the markers.
	stale := map[int64]bool{}
	if maybe, err := s.store.CountPotentiallyStale(r.Context(), year.ID, &neighbor.ID); err == nil && maybe > 0 {
		if rows, err := s.store.RecalcPreview(r.Context(), year.ID, &neighbor.ID); err == nil {
			for _, ro := range rows {
				if ro.Changed {
					stale[ro.EntryID] = true
				}
			}
		}
	}

	// Only active tractors/machines can be booked; inactive ones remain only
	// on historical entries.
	tractors, _ := s.store.ListActiveTractors(r.Context(), base.ID)
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	machines, _ := s.store.ListActiveMachines(r.Context(), base.ID)
	gespanne, _ := s.store.ListGespanne(r.Context(), base.ID)

	data := s.newPage(w, r, neighbor.Name, "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	view := neighborDetailView{
		Stale:        stale,
		StaleCount:   len(stale),
		TaskSummary:  summarizeByTask(entries),
		Completed:    year.Completed(),
		Base:         base,
		Neighbor:     neighbor,
		Entries:      entries,
		BookingCount: len(entries) + len(ledger),
	}
	// Pair links: LinkedFrom gives each machine booking its companion's id (the
	// reverse of the stored direction), PairLabel names the OTHER half for each
	// side — task and hours, so with several pairs on one day the operator sees
	// WHICH booking a link means, not just that one exists. Computed from the
	// rows already loaded — companions always live in the same neighbor+year.
	linkedFrom := map[int64]int64{}
	pairLabel := map[int64]string{}
	byID := map[int64]models.Entry{}
	for _, e := range entries {
		byID[e.ID] = e
	}
	for _, e := range entries {
		if e.LinkedEntryID == nil {
			continue
		}
		machine, ok := byID[*e.LinkedEntryID]
		if !ok {
			continue // partner deleted or filtered — chip falls back to the plain text
		}
		linkedFrom[machine.ID] = e.ID
		// German decimal comma, matching every other number on the page.
		de := func(d decimal.Decimal) string { return strings.ReplaceAll(d.String(), ".", ",") }
		pairLabel[e.ID] = fmt.Sprintf("%s · %s h", machine.TaskLabel, de(machine.Hours))
		pairLabel[machine.ID] = fmt.Sprintf("%s · %s h", e.TaskLabel, de(e.Quantity))
	}
	view.LinkedFrom = linkedFrom
	view.PairLabel = pairLabel
	view.TotalCost = account.Cost
	view.TotalHours = account.Hours
	view.Ledger = ledger
	view.LedgerSum = account.LedgerSum
	view.Saldo = account.Saldo
	view.Payments = account.Payments
	view.PaidSum = account.PaidSum
	plans, err := s.store.ListInstallments(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	view.Installments = installmentViews(plans, account.PaidSum)
	remaining, err := settlements.PayableRemaining(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, "neighbor: payable balance", err)
		return
	}
	view.Remaining = remaining
	// The credit shown on the payout/carry buttons: the negative rest, made
	// positive for display ("Guthaben (45,00 €)").
	view.CreditAmount = remaining.Neg()
	// An issued invoice enables the Skonto (§16) option on the payment form.
	_, invErr := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	view.HasInvoice = invErr == nil
	// Mannstunden (Nr. 56/57) and Anfahrt (Nr. 58) get their own small forms on
	// this page rather than extra fields in the main booking form, whose three
	// stacked submit handlers and pricing fetch are not worth disturbing.
	persons, err := s.store.ActivePersons(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Fotos sichtbar machen (Ausbaukarte 74): a chip per booking and a gallery,
	// so a Wiegeschein is not buried behind a booking's edit page.
	photoCounts, err := s.store.PhotoCounts(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	photos, err := s.store.ListNeighborPhotos(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	ledgerIDs := make([]int64, 0, len(ledger))
	for _, item := range ledger {
		ledgerIDs = append(ledgerIDs, item.ID)
	}
	ledgerPhotoCounts, err := s.store.LedgerPhotoCounts(r.Context(), ledgerIDs)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	view.PhotoCounts = photoCounts
	view.LedgerPhotoCounts = ledgerPhotoCounts
	view.Photos = photos
	view.Persons = persons
	view.TravelFlat = company.TravelFlat
	view.TravelPerKm = company.TravelPerKm
	view.HasTravelRates = company.TravelFlat.IsPositive() || company.TravelPerKm.IsPositive()
	view.Tractors = tractors
	view.Loads = loads
	view.Machines = machines
	view.Gespanne = gespanne
	view.Today = time.Now().Format("2006-01-02")
	bookingValues := newBookingValues()
	if raw := r.URL.Query().Get("machine"); raw != "" {
		if machineID, err := strconv.ParseInt(raw, 10, 64); err == nil && machineID > 0 {
			for _, machine := range machines {
				if machine.ID == machineID {
					bookingValues["mode"] = "manual"
					view.SelectedMachineIDs = []int64{machineID}
					view.BookingPrefilled = true
					machineCopy := machine
					view.PrefilledMachine = &machineCopy
					break
				}
			}
		}
	}
	view.BookingValues = bookingValues
	view.BookingLocked = view.HasInvoice
	view.BookingAction = "/entries"
	view.bind(data)
	s.render(w, r, "neighbor", data)
}

// handleNeighborOverview shows one neighbor across all billing years with cost,
// hours and payment status (payment history).
func (s *Server) handleNeighborOverview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	// One query for the whole history (membership, totals, ledger, paid flag)
	// instead of a 4-queries-per-year fan-out. Cost carries the net (bookings +
	// ledger), same as the dashboard/detail views.
	history, err := s.store.NeighborYearHistory(r.Context(), id)
	if err != nil {
		s.serverError(w, "neighbor year history", err)
		return
	}
	type yearRow struct {
		Year      int
		YearID    int64
		Cost      decimal.Decimal
		Hours     decimal.Decimal
		Remaining decimal.Decimal
		Paid      bool
		Credit    bool
		Completed bool
	}
	rows := make([]yearRow, 0, len(history))
	var totalCost, totalHours decimal.Decimal
	for _, h := range history {
		rows = append(rows, yearRow{
			Year:      h.Year,
			YearID:    h.YearID,
			Cost:      h.Net,
			Hours:     h.Hours,
			Remaining: h.Remaining,
			Paid:      h.Paid,
			Credit:    h.Credit,
			Completed: h.Status == models.YearCompleted,
		})
		totalCost = totalCost.Add(h.Net)
		totalHours = totalHours.Add(h.Hours)
	}
	company, _ := s.store.GetCompany(r.Context())
	// The heading promised a Zahlungshistorie and rendered only year tiles
	// (Ausbaukarte 79) — here are the payments themselves, across all years.
	payments, err := s.store.ListNeighborPayments(r.Context(), neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	var paidTotal decimal.Decimal
	for _, p := range payments {
		paidTotal = paidTotal.Add(p.Amount)
	}
	data := s.newPage(w, r, neighbor.Name+" · Verlauf", "dashboard")
	data["Neighbor"] = neighbor
	data["Rows"] = rows
	data["TotalCost"] = totalCost
	data["TotalHours"] = totalHours
	data["Payments"] = payments
	data["PaidTotal"] = paidTotal
	communication, err := s.store.NeighborCommunication(r.Context(), neighbor.ID, 200)
	if err != nil {
		s.serverError(w, "neighbor communication", err)
		return
	}
	data["Communication"] = communication
	// Mehrjahresverlauf (Ausbaukarte 85): the tiles said what each year was,
	// never where the relationship is going. Same bar-chart partial as the
	// statistics page, one row per year.
	series, err := s.store.NeighborYearSeries(r.Context(), neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	costTrend := make([]aggRow, 0, len(series))
	hoursTrend := make([]aggRow, 0, len(series))
	var costMax, hoursMax decimal.Decimal
	for _, p := range series {
		label := strconv.Itoa(p.Year)
		costTrend = append(costTrend, aggRow{Label: label, Cost: p.Cost,
			URL: fmt.Sprintf("/neighbors/%d?year=%d", neighbor.ID, p.YearID)})
		hoursTrend = append(hoursTrend, aggRow{Label: label, Hours: p.Hours})
		if p.Cost.GreaterThan(costMax) {
			costMax = p.Cost
		}
		if p.Hours.GreaterThan(hoursMax) {
			hoursMax = p.Hours
		}
	}
	data["CostTrend"] = costTrend
	data["CostTrendMax"] = costMax
	data["HoursTrend"] = hoursTrend
	data["HoursTrendMax"] = hoursMax
	data["HasTrend"] = len(series) > 1
	data["Company"] = company
	data["Today"] = time.Now()
	s.render(w, r, "neighbor_overview", data)
}

// handleNeighborOverviewPDF serves the multi-year Kontoauszug as a PDF.
func (s *Server) handleNeighborOverviewPDF(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	history, err := s.store.NeighborYearHistory(r.Context(), id)
	if err != nil {
		s.serverError(w, "overview pdf: history", err)
		return
	}
	company, _ := s.store.GetCompany(r.Context())
	sd := pdf.StatementData{
		IssuerName: company.Name, IssuerAddress: company.Address,
		RecipientName: neighbor.Name, RecipientAddr: neighbor.Address, Today: time.Now(),
	}
	for _, h := range history {
		sd.Rows = append(sd.Rows, pdf.StatementYear{
			Year:      h.Year,
			Cost:      h.Net,
			Hours:     h.Hours,
			Remaining: h.Remaining,
			Paid:      h.Paid,
			Credit:    h.Credit,
		})
		sd.TotalCost = sd.TotalCost.Add(h.Net)
		sd.TotalHours = sd.TotalHours.Add(h.Hours)
	}
	blob, err := pdf.RenderStatement(sd)
	if err != nil {
		s.serverError(w, "overview pdf", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="Kontoauszug_`+sanitizeFilename(neighbor.Name)+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}

// neighborName returns a neighbor's name for audit details, or "#id" if it
// cannot be resolved.
func (s *Server) neighborName(r *http.Request, id int64) string {
	if n, err := s.store.GetNeighbor(r.Context(), id); err == nil {
		return n.Name
	}
	return "#" + strconv.FormatInt(id, 10)
}

// taskSummary aggregates hours and cost per task label (like the Excel columns).
type taskSummary struct {
	Task  string
	Hours decimal.Decimal
	Cost  decimal.Decimal
}

// summarizeByTask groups entries by task label, preserving first-seen order.
func summarizeByTask(entries []models.Entry) []taskSummary {
	order := make([]string, 0)
	byTask := make(map[string]*taskSummary)
	for _, e := range entries {
		label := e.TaskLabel
		if label == "" {
			label = "Sonstige"
		}
		s, ok := byTask[label]
		if !ok {
			s = &taskSummary{Task: label}
			byTask[label] = s
			order = append(order, label)
		}
		s.Hours = s.Hours.Add(e.Hours)
		s.Cost = s.Cost.Add(e.Cost)
	}
	out := make([]taskSummary, 0, len(order))
	for _, l := range order {
		out = append(out, *byTask[l])
	}
	return out
}

// handlePricingAPI returns the pricing data of a base as JSON for live client
// side rate previews in the entry form.
func (s *Server) handlePricingAPI(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	tractors, _ := s.store.ListActiveTractors(r.Context(), id)
	loads, _ := s.store.ListLoadLevels(r.Context(), id)
	machines, _ := s.store.ListActiveMachines(r.Context(), id)
	gespanne, _ := s.store.ListGespanne(r.Context(), id)
	adjustments, _ := s.store.ListFuelAdjustments(r.Context(), id)

	type apiTractor struct {
		ID int64   `json:"id"`
		PS float64 `json:"ps"`
	}
	type apiLoad struct {
		ID   int64   `json:"id"`
		Cost float64 `json:"cost"`
	}
	type apiMachine struct {
		ID   int64   `json:"id"`
		Rate float64 `json:"rate"`
	}
	type apiGespann struct {
		ID       int64   `json:"id"`
		Tractor  *int64  `json:"tractor"`
		Load     *int64  `json:"load"`
		Machines []int64 `json:"machines"`
	}
	type apiAdjustment struct {
		EffectiveFrom string  `json:"effective_from"`
		Label         string  `json:"label"`
		Amount        float64 `json:"amount"`
	}
	out := struct {
		Tractors    []apiTractor    `json:"tractors"`
		Loads       []apiLoad       `json:"loads"`
		Machines    []apiMachine    `json:"machines"`
		Gespanne    []apiGespann    `json:"gespanne"`
		Adjustments []apiAdjustment `json:"adjustments"`
	}{}
	// The pricing API feeds a client-side preview only; float is fine here and
	// keeps the JSON numeric for the JS. The authoritative cost is computed
	// server-side in exact decimals.
	for _, t := range tractors {
		out.Tractors = append(out.Tractors, apiTractor{ID: t.ID, PS: t.PS.InexactFloat64()})
	}
	for _, l := range loads {
		out.Loads = append(out.Loads, apiLoad{ID: l.ID, Cost: l.CostPerPS.InexactFloat64()})
	}
	for _, m := range machines {
		out.Machines = append(out.Machines, apiMachine{ID: m.ID, Rate: calc.MachineRate(m).InexactFloat64()})
	}
	for _, g := range gespanne {
		out.Gespanne = append(out.Gespanne, apiGespann{
			ID: g.ID, Tractor: g.TractorID, Load: g.LoadLevelID, Machines: g.MachineIDs,
		})
	}
	for _, adjustment := range adjustments {
		out.Adjustments = append(out.Adjustments, apiAdjustment{
			EffectiveFrom: adjustment.EffectiveFrom.Format("2006-01-02"),
			Label:         adjustment.Label, Amount: adjustment.AmountPerH.InexactFloat64(),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// invoiceLocked reports whether a festgeschriebene (issued) Rechnung exists for
// this neighbor+year. While one does, the neighbor's bookings and ledger for that
// year are locked (BAO §131 Unveränderbarkeit): corrections go through a
// Storno/Gutschrift, not by editing the frozen basis. Payments and year-close are
// deliberately NOT gated by this — they stay decoupled from invoicing. On a true
// lock (or a lookup error, failing closed) it writes the flash + redirect and
// returns true so the caller just returns.
func (s *Server) invoiceLocked(w http.ResponseWriter, r *http.Request, yearID, neighborID int64) bool {
	iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		s.setFlash(w, r, "error", "Rechnungsstatus konnte nicht geprüft werden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return true
	}
	s.setFlash(w, r, "error", "Rechnung "+iv.Number+" ist festgeschrieben – Buchungen und Verrechnungen für diesen Nachbarn sind gesperrt. Für Korrekturen bitte die Rechnung stornieren.")
	redirect(w, r, neighborURL(neighborID, yearID))
	return true
}

func (s *Server) handleEntryCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if trimmed(r, "booking_form_version") == "2" {
		s.handleBookingCreateV2(w, r)
		return
	}
	if s.handleUnifiedLedgerCreate(w, r) {
		return
	}
	neighborID := formInt64(r, "neighbor_id")
	yearID := formInt64(r, "year_id")
	// An offline replay (offline.js) sets this header and wants a machine-readable
	// status, not a redirect: 2xx = stored, 422 = needs operator attention and
	// stays recoverable in the queue (and 401 from auth = retry after login).
	// This lets the queue distinguish "won't self-heal" from "retry later" instead
	// of treating every redirect as success and silently discarding the booking.
	replay := r.Header.Get("X-Offline-Replay") == "1"
	reject := func(status int, msg, redirectTo string) {
		if replay {
			http.Error(w, msg, status)
			return
		}
		s.setFlash(w, r, "error", msg)
		redirect(w, r, redirectTo)
	}
	idempotencyKey := trimmed(r, "idempotency_key") // set only for offline replays
	if s.tooLong(w, r, "Idempotency-Key", idempotencyKey, maxNameLen) {
		reject(http.StatusUnprocessableEntity, "Idempotency-Key darf höchstens 100 Zeichen lang sein.", neighborURL(neighborID, yearID))
		return
	}
	fingerprint := entryRequestFingerprint(r)
	// Person alongside the rig: only an hour booking books the helper's
	// Mannstunden as a linked companion under a derived key (see below).
	unit := trimmed(r, "unit")
	withCompanion := formInt64(r, "person_id") != 0 && trimmed(r, "booking_kind") != "labor" && (unit == "" || unit == "h")
	// A retry of a stored booking is answered before the year, membership,
	// invoice and catalog checks: a year closed or an invoice issued after a lost
	// answer must not report a saved booking as rejected.
	if idempotencyKey != "" {
		probes := []store.ReplayProbe{{Key: idempotencyKey, Fingerprint: fingerprint}}
		if withCompanion {
			probes = append(probes, store.ReplayProbe{Key: models.CompanionKey(idempotencyKey), Fingerprint: fingerprint})
		}
		if done, _ := s.probeBookingReplay(w, r, probes); done {
			return
		}
	}

	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if errors.Is(err, store.ErrNotFound) {
		s.badRequest(w, "Unbekanntes Abrechnungsjahr")
		return
	} else if err != nil {
		// A real DB error is not "unknown year": surface a 500 so a replay client
		// retries later instead of dropping the booking as a permanent rejection.
		s.serverError(w, "entry create: load year", err)
		return
	}
	if year.Completed() {
		reject(http.StatusUnprocessableEntity, "Das Abrechnungsjahr ist abgeschlossen – es können keine Buchungen mehr erfasst werden.", neighborURL(neighborID, yearID))
		return
	}
	// Inline the neighbor-in-year and invoice-lock checks (rather than the
	// w,r-writing helpers) so the same gates can answer a replay with a status
	// code; the interactive messages/redirect targets are unchanged.
	if in, err := s.store.NeighborInYear(r.Context(), year.ID, neighborID); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	} else if !in {
		reject(http.StatusUnprocessableEntity, "Nachbar ist diesem Abrechnungsjahr nicht zugeordnet.", dashboardURL(yearID))
		return
	}
	if iv, err := s.store.GetInvoice(r.Context(), year.ID, neighborID); err == nil {
		reject(http.StatusUnprocessableEntity, "Rechnung "+iv.Number+" ist festgeschrieben – Buchungen und Verrechnungen für diesen Nachbarn sind gesperrt. Für Korrekturen bitte die Rechnung stornieren.", neighborURL(neighborID, yearID))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		// A real store failure (not "no invoice") is transient — return 500 so an
		// offline replay retries automatically instead of marking it for review.
		s.serverError(w, r.URL.Path, err)
		return
	}

	entry, machineIDs, msg, err := s.resolveUnifiedEntryFromForm(r)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if msg == "" {
		// The same catalog rule as the unified form: the year's price basis only,
		// and no deactivated tractor or machine picked by hand for a new booking.
		msg, err = s.checkBookingCatalog(r, entry, machineIDs, year.Base.ID, entry.GespannID == nil)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
	}
	if msg != "" {
		reject(http.StatusUnprocessableEntity, msg, neighborURL(neighborID, yearID))
		return
	}
	entry.NeighborID = neighborID
	entry.BillingYearID = year.ID
	entry.IdempotencyKey = idempotencyKey
	entry.RequestFingerprint = fingerprint

	// Person alongside the rig (optional): one submit books the machine AND the
	// helper's Mannstunden as a linked companion entry. Hour bookings only — a
	// quantity booking (ha, Ballen, …) carries no hours the helper's time could
	// be derived from; those book their Mannstunden through the dedicated form.
	var companion *models.Entry
	if pid := formInt64(r, "person_id"); pid != 0 && (entry.Unit == "" || entry.Unit == "h") {
		// Quantity bookings (ha, Ballen, …) silently drop a leftover person value:
		// the select lives in the hours-only panel, so on that path it was hidden
		// (and entry-form.js clears it) — a stale value is UI state, not intent,
		// and rejecting would discard everything else the user typed.
		person, err := s.store.GetPerson(r.Context(), pid)
		if errors.Is(err, store.ErrNotFound) {
			reject(http.StatusUnprocessableEntity, "Unbekannte Person.", neighborURL(neighborID, yearID))
			return
		} else if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if !person.HourlyRate.IsPositive() {
			if trimmed(r, "person_rate") == "" {
				reject(http.StatusUnprocessableEntity, "Für "+person.Name+" ist kein Stundensatz hinterlegt — bitte im Personenstamm ergänzen.", neighborURL(neighborID, yearID))
				return
			}
		}
		personHours, personRate := entry.Hours, person.HourlyRate
		for _, field := range []struct {
			key    string
			target *decimal.Decimal
		}{{"person_hours", &personHours}, {"person_rate", &personRate}} {
			if trimmed(r, field.key) != "" {
				value, valid := positiveBookingDecimal(r, field.key)
				if !valid {
					reject(http.StatusUnprocessableEntity, "Bitte gültige Mannstunden und einen positiven Stundensatz angeben.", neighborURL(neighborID, yearID))
					return
				}
				*field.target = value
			}
		}
		personCost := money.Amount(personHours, personRate)
		if !personCost.IsPositive() || personCost.GreaterThanOrEqual(decimal.NewFromInt(10_000_000_000)) {
			reject(http.StatusUnprocessableEntity, "Der Mannstundenbetrag liegt außerhalb des zulässigen Bereichs.", neighborURL(neighborID, yearID))
			return
		}
		companion = &models.Entry{
			NeighborID: neighborID, BillingYearID: year.ID,
			Date: entry.Date, TaskLabel: "Mannstunden " + person.Name,
			Unit: unitMannstunde, Quantity: personHours, UnitPrice: personRate,
			Cost:     personCost,
			PersonID: &person.ID,
			// Derived, deterministic key so a replayed pair no-ops on both halves.
			// Oversized derived keys are hashed to stay within the length limit.
			IdempotencyKey:     models.CompanionKey(idempotencyKey),
			RequestFingerprint: entry.RequestFingerprint,
		}
	}

	// The § 132 BAO trail is written in the creating transaction: a crash after
	// commit can no longer leave a stored booking without it, because the retry
	// deduplicates and would never audit.
	nb := s.neighborName(r, neighborID)
	audit := &store.EntryAudit{}
	if entry.Unit != "" && entry.Unit != "h" {
		audit.Detail = fmt.Sprintf("%s · %s, %s %s × %s = %s €",
			nb, entry.TaskLabel,
			entry.Quantity.String(), entry.Unit, entry.UnitPrice.StringFixed(2), entry.Cost.StringFixed(2))
	} else {
		audit.Detail = fmt.Sprintf("%s · %s, %s h × %s = %s €",
			nb, entry.TaskLabel,
			entry.Hours.StringFixed(2), entry.HourlyRate.StringFixed(2), entry.Cost.StringFixed(2))
	}
	command := store.BookingCommand{
		Entry:      entry,
		MachineIDs: machineIDs,
		Audit:      audit,
	}
	if companion != nil {
		audit.CompanionDetail = fmt.Sprintf("%s · Mannstunden (verknüpft), %s h × %s = %s €",
			nb, companion.Quantity.String(), companion.UnitPrice.StringFixed(2), companion.Cost.StringFixed(2))
		command.Helpers = []*models.Entry{companion}
	}
	result, err := s.store.CreateBooking(r.Context(), command)
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	newID := result.MainID
	companionID := int64(0)
	if len(result.HelperIDs) == 1 {
		companionID = result.HelperIDs[0]
	}
	if newID == 0 && companionID == 0 { // duplicate replay of an offline booking — already recorded
		s.acceptRecordedReplay(w, r, neighborURL(neighborID, yearID))
		return
	}
	if replay {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case newID == 0:
		s.setFlash(w, r, "success", "Mannstunden zur bereits erfassten Buchung ergänzt.")
	case companionID != 0:
		s.setFlash(w, r, "success", "Buchung + Mannstunden gespeichert.")
	default:
		s.setFlash(w, r, "success", "Buchung gespeichert.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// resolveEntryFromForm reads the booking form fields, resolves the tractor,
// load level and machines (from a fixed gespann or manual selection) and
// returns a populated Entry (without neighbor/year) plus its machine ids. On
// validation failure it returns a non-empty German message. Only a missing
// catalog item is a validation failure; any other store error is returned as an
// error, so the caller answers 500 and an offline replay retries instead of
// turning a database blip into a permanent rejection.
func (s *Server) resolveEntryFromForm(r *http.Request) (*models.Entry, []int64, string, error) {
	// A replay may run days after the capture: an empty or malformed date must
	// be corrected, never silently become the day of the replay.
	entryDate, dateErr := time.Parse("2006-01-02", trimmed(r, "entry_date"))
	const invalidDate = "Bitte ein gültiges Datum angeben."
	// Non-hour unit (ha, Ballen, m³, …): quantity × unit price, no rig required.
	// Hours stay 0 (they don't count toward TotalHours). Unit "h" (or empty) falls
	// through to the rig-based hourly path below. "__custom" resolves to the
	// free-text unit field.
	unit := trimmed(r, "unit")
	if unit == "__custom" {
		unit = trimmed(r, "unit_custom")
		if unit == "" {
			return nil, nil, "Bitte eine eigene Einheit angeben.", nil
		}
	}
	if unit != "" && unit != "h" {
		if msg := lenError("Einheit", unit, 16); msg != "" {
			return nil, nil, msg, nil
		}
		taskLabel := trimmed(r, "task_label")
		if taskLabel == "" {
			return nil, nil, "Bitte eine Tätigkeit angeben.", nil
		}
		if msg := lenError("Tätigkeit", taskLabel, maxNameLen); msg != "" {
			return nil, nil, msg, nil
		}
		quantity := formDecimal(r, "quantity")
		if !quantity.IsPositive() {
			return nil, nil, "Menge muss größer als 0 sein.", nil
		}
		unitPrice := formDecimal(r, "unit_price")
		if !unitPrice.IsPositive() {
			return nil, nil, "Preis je Einheit muss größer als 0 sein.", nil
		}
		note := trimmed(r, "note")
		if msg := lenError("Notiz", note, maxNoteLen); msg != "" {
			return nil, nil, msg, nil
		}
		if dateErr != nil {
			return nil, nil, invalidDate, nil
		}
		return &models.Entry{
			Date:       entryDate,
			TaskLabel:  taskLabel,
			Unit:       unit,
			Quantity:   quantity,
			UnitPrice:  unitPrice,
			Hours:      decimal.Zero,
			HourlyRate: decimal.Zero,
			Cost:       calc.Cost(quantity, unitPrice),
			Note:       note,
		}, nil, "", nil
	}

	machineIDs, ok := formMachineIDs(r)
	if !ok {
		return nil, nil, "Zu viele Maschinen auf einmal.", nil
	}
	selection := equipmentSelection{
		TractorID:   formInt64Ptr(r, "tractor_id"),
		LoadLevelID: formInt64Ptr(r, "load_level_id"),
		MachineIDs:  machineIDs,
		TaskLabel:   trimmed(r, "task_label"),
	}
	if r.FormValue("mode") != "manual" {
		if gid := formInt64(r, "gespann_id"); gid != 0 {
			selection.GespannID = &gid
		} else {
			selection.TractorID, selection.LoadLevelID = nil, nil
		}
	}
	resolved, failure, err := s.resolveEquipment(r.Context(), selection)
	if err != nil {
		return nil, nil, "", err
	}
	switch failure {
	case equipmentResolutionIncompletePair:
		return nil, nil, "Traktor und Belastungsstufe gehören zusammen — bitte beides wählen oder beides leer lassen.", nil
	case equipmentResolutionEmpty:
		return nil, nil, "Bitte Traktor und Belastungsstufe oder mindestens eine Maschine wählen.", nil
	case equipmentResolutionGespannMissing:
		return nil, nil, "Das gewählte Gespann ist nicht mehr vorhanden — bitte neu wählen.", nil
	case equipmentResolutionTractorMissing:
		return nil, nil, "Traktor nicht gefunden.", nil
	case equipmentResolutionLoadMissing:
		return nil, nil, "Belastungsstufe nicht gefunden.", nil
	case equipmentResolutionMachineMissing:
		return nil, nil, "Die gewählten Maschinen sind nicht mehr verfügbar — bitte die Seite neu laden.", nil
	}
	entry, ids := resolved.entrySnapshot()
	taskLabel := entry.TaskLabel
	hours := formDecimal(r, "hours")
	if !hours.IsPositive() {
		return nil, nil, "Stunden müssen größer als 0 sein.", nil
	}
	if !store.MachineHoursRepresentable(hours) {
		return nil, nil, "Maschinenstunden aus dem gepflegten Pool erlauben höchstens drei Nachkommastellen.", nil
	}
	if dateErr != nil {
		return nil, nil, invalidDate, nil
	}
	if msg := lenError("Tätigkeit", taskLabel, maxNameLen); msg != "" {
		return nil, nil, msg, nil
	}
	note := trimmed(r, "note")
	if msg := lenError("Notiz", note, maxNoteLen); msg != "" {
		return nil, nil, msg, nil
	}
	entry.Date = entryDate
	if err := s.applyFuelAdjustment(r.Context(), resolved.baseID(), entryDate, &entry); err != nil {
		return nil, nil, "", err
	}
	entry.Hours = hours
	entry.Cost = calc.Cost(hours, entry.HourlyRate)
	entry.Note = note
	return &entry, ids, "", nil
}

// entryUpdateDetail renders a per-field old→new summary of an edited booking so
// the audit trail shows what actually changed, not just the resulting cost.
func entryUpdateDetail(prev, cur *models.Entry) string {
	d := diffFields(
		fieldChange{"Datum", prev.Date.Format("02.01.2006"), cur.Date.Format("02.01.2006")},
		fieldChange{"Tätigkeit", prev.TaskLabel, cur.TaskLabel},
		fieldChange{"Maschinen", prev.MachineLabels, cur.MachineLabels},
		fieldChange{"Einheit", prev.Unit, cur.Unit},
		fieldChange{"Menge", prev.Quantity.StringFixed(2), cur.Quantity.StringFixed(2)},
		fieldChange{"Einzelpreis", prev.UnitPrice.StringFixed(2), cur.UnitPrice.StringFixed(2)},
		fieldChange{"Stunden", prev.Hours.StringFixed(2), cur.Hours.StringFixed(2)},
		fieldChange{"Satz", prev.HourlyRate.StringFixed(2), cur.HourlyRate.StringFixed(2)},
		fieldChange{"Kosten", prev.Cost.StringFixed(2) + " €", cur.Cost.StringFixed(2) + " €"},
		fieldChange{"Notiz", prev.Note, cur.Note},
	)
	if d == "" {
		// Mirror the booking's own basis: hours × rate for time bookings, but
		// quantity × unit price for a unit-based one (h × rate would be 0 there).
		if cur.Unit != "" && cur.Unit != "h" {
			return fmt.Sprintf("keine inhaltliche Änderung (%s %s × %s = %s €)",
				cur.Quantity.StringFixed(2), cur.Unit, cur.UnitPrice.StringFixed(2), cur.Cost.StringFixed(2))
		}
		return fmt.Sprintf("keine inhaltliche Änderung (%s h × %s = %s €)",
			cur.Hours.StringFixed(2), cur.HourlyRate.StringFixed(2), cur.Cost.StringFixed(2))
	}
	return d
}

// handleEntryUpdate edits an existing booking (only while the year is open).
func (s *Server) handleEntryUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	existing, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.entryYearOpen(w, r, existing, "Das Abrechnungsjahr ist abgeschlossen – Buchungen können nicht mehr geändert werden.") {
		return
	}
	if trimmed(r, "booking_form_version") == "2" {
		s.updateBookingEntryV2(w, r, existing)
		return
	}
	kind, direction, selectionMsg := unifiedBookingSelection(r)
	if selectionMsg != "" || direction == "in" || kind == "fixed" {
		s.setFlash(w, r, "error", "Die Verrechnungsrichtung einer bestehenden Leistung bleibt erhalten. Bitte bei Bedarf stornieren und neu erfassen.")
		redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
		return
	}
	entry, machineIDs, msg, err := s.resolveUnifiedEntryFromForm(r)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if msg == "" {
		// Edits stay on the year's price basis too, but may keep a tractor or
		// machine that was deactivated after it was booked (as V2 edits do).
		year, err := s.store.GetBillingYear(r.Context(), existing.BillingYearID)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if msg, err = s.checkBookingCatalog(r, entry, machineIDs, year.Base.ID, false); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
	}
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
		return
	}
	// Validate the target column before saving either half. Otherwise a four-
	// decimal labor quantity would save first, then round only machine hours.
	if r.FormValue("sync_pair") == "1" {
		partnerID, err := s.store.LinkedPartnerID(r.Context(), id)
		if err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		}
		if partnerID != 0 {
			partner, err := s.store.GetEntry(r.Context(), partnerID)
			if err != nil {
				s.serverError(w, r.URL.Path, err)
				return
			}
			hours := entry.Hours
			if entry.Unit != "" && entry.Unit != "h" {
				hours = entry.Quantity
			}
			if partner.Unit == "h" && !store.MachineHoursRepresentable(hours) {
				s.setFlash(w, r, "error", "Zum Angleichen der verknüpften Maschine sind höchstens drei Nachkommastellen und weniger als 10 Millionen Stunden möglich. Beide Buchungen bleiben unverändert.")
				redirect(w, r, "/entries/"+itoa64(id)+"/edit")
				return
			}
		}
	}
	entry.ID = id
	if entry.Unit == models.UnitMannstunde && entry.PersonID == nil {
		entry.PersonID = existing.PersonID
	}
	// Linked pair: mirror the edited hours onto the partner when the edit form's
	// checkbox asked for it — machine and Mannstunden of one Einsatz share the
	// same hours, each priced at its own frozen rate. Both halves are written in
	// one locked transaction, so an invoice can never freeze a half-synced pair.
	pair, err := s.store.UpdateEntryWithPartner(r.Context(), entry, machineIDs, r.FormValue("sync_pair") == "1")
	if errors.Is(err, store.ErrPairHoursPrecision) {
		s.setFlash(w, r, "error", "Zum Angleichen der verknüpften Maschine sind höchstens drei Nachkommastellen und weniger als 10 Millionen Stunden möglich. Beide Buchungen bleiben unverändert.")
		redirect(w, r, "/entries/"+itoa64(id)+"/edit")
		return
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "update", "entry", id, s.neighborName(r, existing.NeighborID)+" · "+entryUpdateDetail(existing, entry))
	if pair.PartnerID != 0 {
		s.audit(r, "update", "entry", pair.PartnerID, fmt.Sprintf("%s · verknüpft angeglichen: %s h, %s €",
			s.neighborName(r, existing.NeighborID), pair.Hours.String(), pair.Cost.StringFixed(2)))
		s.setFlash(w, r, "success", "Buchung und verknüpfte Buchung aktualisiert.")
		redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
		return
	}
	s.setFlash(w, r, "success", "Buchung aktualisiert.")
	redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
}

// handleEntryVoid cancels or restores a booking (traceable alternative to
// deletion). Voided bookings are excluded from totals.
func (s *Server) handleEntryVoid(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.entryYearOpen(w, r, entry, "Das Abrechnungsjahr ist abgeschlossen.") {
		return
	}
	void := r.FormValue("voided") == "true"
	reason := trimmed(r, "reason")
	if s.tooLong(w, r, "Grund", reason, maxNoteLen) {
		redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
		return
	}
	nb := s.neighborName(r, entry.NeighborID)
	// Linked pair: mirror the action onto the partner only on the confirm
	// dialog's explicit choice. Void is reversible, so two separate updates are
	// acceptable — a failure between them stays visible and re-clickable.
	partnerID := int64(0)
	if r.FormValue("cascade") == "1" {
		if pid, err := s.store.LinkedPartnerID(r.Context(), id); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		} else {
			partnerID = pid
		}
	}
	if err := s.store.SetEntryVoided(r.Context(), id, void, reason); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else {
		suffix := ""
		if partnerID != 0 {
			if err := s.store.SetEntryVoided(r.Context(), partnerID, void, reason); err != nil {
				s.setFlash(w, r, "error", "Verknüpfte Buchung konnte nicht angepasst werden.")
				redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
				return
			}
			suffix = " (verknüpftes Paar)"
		}
		if void {
			s.audit(r, "void", "entry", id, fmt.Sprintf("%s · %s € %s%s", nb, entry.Cost.StringFixed(2), reason, suffix))
			if partnerID != 0 {
				s.audit(r, "void", "entry", partnerID, fmt.Sprintf("%s · %s%s", nb, reason, suffix))
				s.setFlash(w, r, "success", "Buchung und verknüpfte Buchung storniert.")
			} else {
				s.setFlash(w, r, "success", "Buchung storniert.")
			}
		} else {
			s.audit(r, "unvoid", "entry", id, fmt.Sprintf("%s · %s €%s", nb, entry.Cost.StringFixed(2), suffix))
			if partnerID != 0 {
				s.audit(r, "unvoid", "entry", partnerID, nb+suffix)
				s.setFlash(w, r, "success", "Stornierung beider Buchungen aufgehoben.")
			} else {
				s.setFlash(w, r, "success", "Stornierung aufgehoben.")
			}
		}
	}
	redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
}

// ledgerFormValues parses the shared add/edit fields: a positive amount plus a
// direction ("credit" = I owe the neighbor → stored negative), a description
// and an optional posting date (defaults to today). Returns a user-facing
// message when the amount is invalid.
func ledgerFormValues(r *http.Request) (amount decimal.Decimal, description string, date time.Time, msg string) {
	amount = formDecimal(r, "amount").Abs()
	if !amount.IsPositive() {
		return amount, "", date, "Bitte einen Betrag größer 0 angeben."
	}
	if models.HasSubCent(amount) {
		return amount, "", date, msgMoneyCents
	}
	if r.FormValue("direction") == "credit" {
		amount = amount.Neg() // I owe the neighbor → reduces the balance
	}
	description = trimmed(r, "description")
	if msg := lenError("Beschreibung", description, maxNoteLen); msg != "" {
		return amount, "", date, msg
	}
	date, err := time.Parse("2006-01-02", trimmed(r, "posting_date"))
	if err != nil {
		date = time.Now()
	}
	return amount, description, date, ""
}

// ledgerYearOpen reports whether the billing year is still open. It fails
// closed: a lookup error also blocks the mutation (never silently proceed on a
// possibly-completed or missing year).
func (s *Server) ledgerYearOpen(w http.ResponseWriter, r *http.Request, yearID, neighborID int64) bool {
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.setFlash(w, r, "error", "Abrechnungsjahr konnte nicht geladen werden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return false
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return false
	}
	if s.invoiceLocked(w, r, yearID, neighborID) {
		return false
	}
	return true
}

// transferYearsOpen reports whether every billing year a carry-forward transfer
// touches is still open. Undoing a transfer changes both sides at once, so it
// must be blocked when either the clicked or the linked year is completed —
// ledgerYearOpen only guards the clicked side.
func (s *Server) transferYearsOpen(w http.ResponseWriter, r *http.Request, transferID string, neighborID, yearID int64) bool {
	ids, err := s.store.LedgerTransferYearIDs(r.Context(), transferID)
	if err != nil {
		s.setFlash(w, r, "error", "Übertrag konnte nicht geladen werden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return false
	}
	for _, id := range ids {
		year, err := s.store.GetBillingYear(r.Context(), id)
		if err != nil {
			s.setFlash(w, r, "error", "Abrechnungsjahr konnte nicht geladen werden.")
			redirect(w, r, neighborURL(neighborID, yearID))
			return false
		}
		if year.Completed() {
			s.setFlash(w, r, "error", "Ein beteiligtes Abrechnungsjahr ist abgeschlossen.")
			redirect(w, r, neighborURL(neighborID, yearID))
			return false
		}
	}
	return true
}

// entryYearOpen reports whether the entry's billing year is still open. Like
// ledgerYearOpen it fails closed: a year-lookup error blocks the mutation
// instead of silently proceeding on a possibly-completed (settled) year.
func (s *Server) entryYearOpen(w http.ResponseWriter, r *http.Request, e *models.Entry, blockedMsg string) bool {
	year, err := s.store.GetBillingYear(r.Context(), e.BillingYearID)
	if err != nil {
		s.setFlash(w, r, "error", "Abrechnungsjahr konnte nicht geladen werden.")
		redirect(w, r, neighborURL(e.NeighborID, e.BillingYearID))
		return false
	}
	if year.Completed() {
		s.setFlash(w, r, "error", blockedMsg)
		redirect(w, r, neighborURL(e.NeighborID, e.BillingYearID))
		return false
	}
	if s.invoiceLocked(w, r, e.BillingYearID, e.NeighborID) {
		return false
	}
	return true
}

// handleLedgerAdd records a manual account posting for a neighbor in a year.
func (s *Server) handleLedgerAdd(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	if !s.ledgerYearOpen(w, r, yearID, neighborID) {
		return
	}
	// Only members of the year may get postings, otherwise an orphan posting
	// would count in the year ledger total but not in the per-neighbor / payment
	// views (which join billing_year_neighbors), skewing the stats.
	// A lookup error is NOT "not a member": surface it instead of masking a DB
	// problem behind a data-sounding message.
	member, err := s.store.NeighborInYear(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "ledger add: membership lookup", err)
		return
	}
	if !member {
		s.setFlash(w, r, "error", "Nachbar ist in diesem Abrechnungsjahr nicht vorhanden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	amount, description, date, msg := ledgerFormValues(r)
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if _, err := s.store.AddNeighborLedger(r.Context(), yearID, neighborID, amount, description, date); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		s.setFlash(w, r, "success", "Position hinzugefügt.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handleLedgerEditForm renders the edit form for one posting.
func (s *Server) handleLedgerEditForm(w http.ResponseWriter, r *http.Request) {
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
	data := s.newPage(w, r, "Position bearbeiten", "dashboard")
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
	if err := s.setLedgerBookingForm(r, data, &e, year, neighborID, false); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.render(w, r, "ledger_edit", data)
}

// handleLedgerUpdate saves an edited posting (while the year is open).
func (s *Server) handleLedgerUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID, neighborID, existing, err := s.store.GetLedgerEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.ledgerYearOpen(w, r, yearID, neighborID) {
		return
	}
	// A carry-forward side only ever changes together with its counterpart
	// (void or undo the whole transfer); editing one side broke the zero sum.
	if existing.TransferID != "" {
		s.setFlash(w, r, "error", msgLedgerTransferEdit)
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if existing.Booking != nil {
		if trimmed(r, "booking_form_version") == "2" {
			s.updateBookingLedgerV2(w, r, &existing, yearID, neighborID)
			return
		}
		r.Form.Set("neighbor_id", itoa64(neighborID))
		r.Form.Set("year_id", itoa64(yearID))
		kind, direction, msg := unifiedBookingSelection(r)
		if kind != existing.Booking.Kind || (direction == "in") != existing.Amount.IsNegative() {
			msg = "Buchungsart und Verrechnungsrichtung bleiben beim Bearbeiten erhalten. Bitte bei Bedarf stornieren und neu erfassen."
		}
		if msg == "" && direction == "out" && kind != "fixed" {
			msg = "Eine Gegenleistung kann nicht nachträglich in eine eigene Rechnungsleistung umgewandelt werden. Bitte stornieren und neu erfassen."
		}
		if msg != "" {
			s.rejectUnifiedBooking(w, r, msg)
			return
		}
		in, msg := ledgerBookingFromForm(r, kind, direction)
		if msg != "" {
			s.rejectUnifiedBooking(w, r, msg)
			return
		}
		if err := s.store.UpdateLedgerBooking(r.Context(), id, in); err != nil {
			s.unifiedBookingError(w, r, err)
			return
		}
		s.setFlash(w, r, "success", "Position aktualisiert.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	amount, description, date, msg := ledgerFormValues(r)
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.UpdateNeighborLedger(r.Context(), id, amount, description, date); errors.Is(err, store.ErrLedgerTransfer) {
		s.setFlash(w, r, "error", msgLedgerTransferEdit)
	} else if err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		s.setFlash(w, r, "success", "Position aktualisiert.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handleLedgerVoid cancels or restores a posting (traceable alternative to
// deletion; a voided posting stays visible but is excluded from the balance).
func (s *Server) handleLedgerVoid(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID, neighborID, e, err := s.store.GetLedgerEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.ledgerYearOpen(w, r, yearID, neighborID) {
		return
	}
	void := r.FormValue("voided") == "true"
	reason := trimmed(r, "reason")
	if s.tooLong(w, r, "Grund", reason, maxNoteLen) {
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	// A carry-forward posting reverses as a unit: void/restore both sides so the
	// balance can't be left settled in one year and gone from the other.
	if e.TransferID != "" {
		if !s.transferYearsOpen(w, r, e.TransferID, neighborID, yearID) {
			return
		}
		if err := s.store.SetLedgerVoidedTransfer(r.Context(), e.TransferID, void, reason); err != nil {
			s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
		} else if void {
			s.setFlash(w, r, "success", "Übertrag storniert — beide Seiten aufgehoben.")
		} else {
			s.setFlash(w, r, "success", "Übertrags-Stornierung aufgehoben.")
		}
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.SetLedgerVoided(r.Context(), id, void, reason); errors.Is(err, store.ErrLedgerTransfer) {
		s.setFlash(w, r, "error", msgLedgerTransferEdit)
	} else if err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if void {
		s.setFlash(w, r, "success", "Position storniert.")
	} else {
		s.setFlash(w, r, "success", "Stornierung aufgehoben.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handleLedgerDelete removes a manual posting (while the year is open).
func (s *Server) handleLedgerDelete(w http.ResponseWriter, r *http.Request) {
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
	if !s.ledgerYearOpen(w, r, yearID, neighborID) {
		return
	}
	// A carry-forward posting reverses as a unit: deleting one side removes both,
	// so the balance reopens in the source year instead of vanishing.
	if e.TransferID != "" {
		if !s.transferYearsOpen(w, r, e.TransferID, neighborID, yearID) {
			return
		}
		if err := s.store.DeleteLedgerTransfer(r.Context(), e.TransferID); err != nil {
			s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
		} else {
			s.setFlash(w, r, "success", "Übertrag rückgängig gemacht — der Rest ist im anderen Jahr wieder offen.")
		}
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.DeleteNeighborLedger(r.Context(), id); errors.Is(err, store.ErrLedgerTransfer) {
		s.setFlash(w, r, "error", msgLedgerTransferEdit)
	} else if err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.setFlash(w, r, "success", "Position entfernt.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// knownUnits are the booking form's named unit options. Anything else that isn't
// empty is a custom (free-text) unit — the form must select "Andere Einheit" and
// prefill the free-text field, or the select would silently fall back to "h".
var knownUnits = map[string]bool{"h": true, "ha": true, "Ballen": true, "m³": true, "Fuhre": true, "t": true}

func unitIsCustom(u string) bool { return u != "" && !knownUnits[u] }

// handleEntryEditForm renders a prefilled booking form for editing.
func (s *Server) handleEntryEditForm(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), entry.NeighborID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), entry.BillingYearID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, neighborURL(neighbor.ID, year.ID))
		return
	}
	base := year.Base
	tractors, _ := s.store.ListTractors(r.Context(), base.ID)
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	machines, _ := s.store.ListMachines(r.Context(), base.ID)
	gespanne, _ := s.store.ListGespanne(r.Context(), base.ID)
	selMachines, _ := s.store.EntryMachineIDs(r.Context(), id)

	photos, _ := s.store.ListEntryPhotos(r.Context(), id)
	data := s.newPage(w, r, "Buchung bearbeiten", "dashboard")
	data["Entry"] = entry
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Base"] = base
	data["Tractors"] = tractors
	data["Loads"] = loads
	data["Machines"] = machines
	data["Gespanne"] = gespanne
	data["SelectedMachineIDs"] = selMachines
	data["Photos"] = photos
	data["UnitIsCustom"] = unitIsCustom(entry.Unit)
	data["IsQtyEntry"] = entry.Unit != "" && entry.Unit != "h"
	persons, err := s.store.ListPersons(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Persons"] = persons
	data["NextWeek"] = time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	// Linked pair: name the partner so the form can offer to mirror edited hours.
	if pid, err := s.store.LinkedPartnerID(r.Context(), id); err == nil && pid != 0 {
		if partner, err := s.store.GetEntry(r.Context(), pid); err == nil {
			label := partner.TaskLabel
			if label == "" {
				label = "verknüpfte Buchung"
			}
			data["PairPartnerLabel"] = label
			// Only the helper half can be carried into a series (the machine half
			// IS the series), only while it still stands (a stornierte companion
			// must not come back every week), and only for an hour booking — the
			// companion is priced over the machine booking's hours, which a
			// quantity booking does not have. Named separately so the series card
			// offers the choice only where it can be honored.
			hourly := entry.Unit == "" || entry.Unit == "h"
			if partner.PersonID != nil && partner.UnitPrice.IsPositive() && !partner.Voided && hourly {
				data["PairPersonLabel"] = label
			}
		}
	}
	if err := s.setEntryBookingForm(r, data, entry, false); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	_, invErr := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	data["BookingLocked"] = year.Completed() || !errors.Is(invErr, store.ErrNotFound)
	s.render(w, r, "entry_edit", data)
}

// handleEntryCopy renders the entry form pre-filled from an existing booking as a
// NEW booking (dated today), so a recurring task can be re-entered in one click.
// It reuses the entry_edit template with Copy=true, which points the form at the
// create route and adds the neighbor/year hidden fields.
func (s *Server) handleEntryCopy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), entry.NeighborID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), entry.BillingYearID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, neighborURL(neighbor.ID, year.ID))
		return
	}
	entry.Date = time.Now() // a copy is a fresh booking for today
	base := year.Base
	tractors, _ := s.store.ListTractors(r.Context(), base.ID)
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	machines, _ := s.store.ListMachines(r.Context(), base.ID)
	gespanne, _ := s.store.ListGespanne(r.Context(), base.ID)
	selMachines, _ := s.store.EntryMachineIDs(r.Context(), id)

	data := s.newPage(w, r, "Buchung kopieren", "dashboard")
	data["Entry"] = entry
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Base"] = base
	data["Tractors"] = tractors
	data["Loads"] = loads
	data["Machines"] = machines
	data["Gespanne"] = gespanne
	data["SelectedMachineIDs"] = selMachines
	data["UnitIsCustom"] = unitIsCustom(entry.Unit)
	data["IsQtyEntry"] = entry.Unit != "" && entry.Unit != "h"
	persons, err := s.store.ListPersons(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Persons"] = persons
	data["Copy"] = true
	if err := s.setEntryBookingForm(r, data, entry, true); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	_, invErr := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	data["BookingLocked"] = year.Completed() || !errors.Is(invErr, store.ErrNotFound)
	s.render(w, r, "entry_edit", data)
}

// quickRow is one filled Schnellerfassung row and its outcome.
type quickRow struct {
	index                             int
	key                               string
	gespannID, personID               int64
	hours                             decimal.Decimal
	date                              time.Time
	fingerprint, companionFingerprint string
	// status is "" while pending, else quickSaved, quickInvalid or quickConflict.
	status, message string
}

// Per-row outcomes, reported to an offline replay by row key.
const (
	quickSaved    = "saved"
	quickInvalid  = "invalid"
	quickConflict = "conflict"
)

// quickRowStatus is the JSON shape of one row's outcome in a replay answer.
type quickRowStatus struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// handleQuickEntries creates several bookings at once from the quick-entry rows
// (date, fixed gespann, hours).
func (s *Server) handleQuickEntries(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	neighborID := formInt64(r, "neighbor_id")
	yearID := formInt64(r, "year_id")
	// An offline replay wants a machine-readable status, not a redirect — same
	// contract as handleEntryCreate. The client reads the response with
	// redirect:"manual", so a 303 arrives as an opaque status 0 that is neither
	// success nor permanent rejection: the batch would sit in the queue forever,
	// re-POSTed on every page load, with the badge stuck.
	replay := r.Header.Get("X-Offline-Replay") == "1"
	var rows []*quickRow
	// rejectRows answers a replay with the status of every row by key, so the
	// queue can show which rows are stored and keep those read-only.
	rejectRows := func(msg string) {
		if !strings.Contains(r.Header.Get("Accept"), "application/json") {
			http.Error(w, msg, http.StatusUnprocessableEntity)
			return
		}
		states := map[string]quickRowStatus{}
		for _, row := range rows {
			if row.key != "" && row.status != "" {
				states[row.key] = quickRowStatus{Status: row.status, Message: row.message}
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(struct {
			Message string                    `json:"message"`
			Rows    map[string]quickRowStatus `json:"rows"`
		}{msg, states})
	}
	reject := func(status int, msg, redirectTo string) {
		if replay {
			if status == http.StatusUnprocessableEntity {
				rejectRows(msg)
				return
			}
			http.Error(w, msg, status)
			return
		}
		s.setFlash(w, r, "error", msg)
		redirect(w, r, redirectTo)
	}
	// Every accepted row costs five database round trips — GetGespann, GetTractor,
	// GetLoadLevel and MachinesByIDs inside buildGespannEntry, then CreateEntry —
	// and the loop below is driven purely by how many q_gespann keys arrive. The
	// body is capped at 1 MiB (limitBody), which still fits on the order of twenty
	// thousand rows, so one request could drive six figures of queries. The form
	// sends exactly six and offers no way to add a row, so anything past this
	// ceiling did not come from it.
	//
	// Reject rather than truncate: these rows are business records. Silently
	// dropping everything past a cap would report "N Buchungen gespeichert" while
	// discarding the rest, which is worse than refusing the request outright. The
	// check sits ahead of every store call so an abusive submit costs no queries
	// at all.
	if n := len(r.Form["q_gespann"]); n > maxQuickEntries {
		reject(http.StatusUnprocessableEntity, fmt.Sprintf("Zu viele Zeilen auf einmal (%d). Es können höchstens %d Zeilen gespeichert werden.", n, maxQuickEntries), neighborURL(neighborID, yearID))
		return
	}

	dates := r.Form["q_date"]
	gespanne := r.Form["q_gespann"]
	hoursList := r.Form["q_hours"]
	// Offline replay (Ausbaukarte 100): the client sends one key PER ROW, since
	// one submit becomes N bookings and a single key would let a replay create
	// only the first of them.
	keys := r.Form["q_key"]
	// A row may name a helper, exactly like the single booking form's person
	// select: the row then books the machine AND that helper's Mannstunden as a
	// linked companion, over the row's own hours. Empty when no helper is
	// configured — the column is not rendered at all then.
	personIDs := r.Form["q_person"]
	at := func(list []string, i int) string {
		if i < len(list) {
			return strings.TrimSpace(list[i])
		}
		return ""
	}
	// Parse every row before any store call: validation and the replay check
	// need nothing but the form.
	for i := range gespanne {
		rawGespann, rawHours := at(gespanne, i), at(hoursList, i)
		if rawGespann == "" && rawHours == "" {
			continue // an untouched blank row of the fixed table — not an error
		}
		row := &quickRow{index: i}
		if key := at(keys, i); len(key) <= maxNameLen {
			row.key = key
		}
		row.gespannID, _ = strconv.ParseInt(rawGespann, 10, 64)
		row.hours = parseGermanDecimal(rawHours)
		rawPerson := at(personIDs, i)
		row.personID, _ = strconv.ParseInt(rawPerson, 10, 64)
		dateStr := at(dates, i)
		row.fingerprint = quickRowFingerprint(neighborID, yearID, dateStr, rawGespann, rawHours, "")
		if row.personID != 0 {
			row.companionFingerprint = quickRowFingerprint(neighborID, yearID, dateStr, rawGespann, rawHours, rawPerson)
		}
		var err error
		row.date, err = time.Parse("2006-01-02", dateStr)
		switch {
		case row.gespannID == 0 || !row.hours.IsPositive():
			// The row WAS filled in but does not parse ("1.234,5", missing rig):
			// silently dropping it reported "N Buchungen gespeichert" while a
			// day of work vanished. Count it and say so.
			row.status, row.message = quickInvalid, "Gespann und gültige Stunden erforderlich."
		case !store.MachineHoursRepresentable(row.hours):
			row.status, row.message = quickInvalid, "Maschinenstunden erlauben höchstens drei Nachkommastellen."
		case err != nil:
			// A replay may run days later: never book "today" instead.
			row.status, row.message = quickInvalid, "Bitte ein gültiges Datum angeben."
		}
		rows = append(rows, row)
	}

	// Rows already stored under their key are settled before the year,
	// membership and invoice gates: an invoice issued or a year closed after a
	// lost answer must not report a saved batch as rejected. A key that holds
	// different data is a conflict, never a silent no-op.
	var probes []store.ReplayProbe
	var probeRows []*quickRow
	for _, row := range rows {
		if row.key == "" {
			continue
		}
		probes = append(probes, store.ReplayProbe{Key: row.key, Fingerprint: row.fingerprint})
		probeRows = append(probeRows, row)
		if row.personID != 0 {
			probes = append(probes, store.ReplayProbe{Key: models.CompanionKey(row.key), Fingerprint: row.companionFingerprint})
			probeRows = append(probeRows, row)
		}
	}
	states, err := s.store.ProbeReplay(r.Context(), yearID, neighborID, probes)
	if err != nil {
		s.serverError(w, "quick entries: replay check", err)
		return
	}
	recorded := map[*quickRow]bool{}
	for i, state := range states {
		row := probeRows[i]
		switch state {
		case store.ReplayRecorded:
			if _, seen := recorded[row]; !seen {
				recorded[row] = true
			}
		case store.ReplayDiffers, store.ReplayLedger, store.ReplayForeign:
			row.status, row.message = quickConflict, replayConflictMsg
			recorded[row] = false
		default:
			recorded[row] = false
		}
	}
	already := 0
	for row, complete := range recorded {
		if complete {
			row.status, row.message = quickSaved, ""
			already++
		}
	}
	pending := 0
	for _, row := range rows {
		if row.status == "" {
			pending++
		}
	}

	created, paired := 0, 0
	var createErr error
	if pending > 0 {
		year, err := s.store.GetBillingYear(r.Context(), yearID)
		if errors.Is(err, store.ErrNotFound) {
			s.badRequest(w, "Unbekanntes Abrechnungsjahr")
			return
		} else if err != nil {
			s.serverError(w, "quick entries: load year", err)
			return
		}
		markPending := func(msg string) {
			for _, row := range rows {
				if row.status == "" {
					row.status, row.message = quickInvalid, msg
				}
			}
		}
		if year.Completed() {
			markPending("Das Abrechnungsjahr ist abgeschlossen.")
			reject(http.StatusUnprocessableEntity, "Das Abrechnungsjahr ist abgeschlossen.", neighborURL(neighborID, yearID))
			return
		}
		// Inlined rather than routed through a w,r-writing helper (as invoiceLocked
		// still is elsewhere) so these gates can answer a replay with a status code —
		// the same reason handleEntryCreate inlines them. The interactive messages and
		// redirect targets are unchanged.
		//
		// The membership check guards against orphan rows: a booking for a neighbor not
		// in the year is invisible on the (membership-driven) dashboard yet counted by
		// stats/CSV, skewing the year's totals.
		if in, err := s.store.NeighborInYear(r.Context(), year.ID, neighborID); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		} else if !in {
			markPending("Nachbar ist diesem Abrechnungsjahr nicht zugeordnet.")
			reject(http.StatusUnprocessableEntity, "Nachbar ist diesem Abrechnungsjahr nicht zugeordnet.", dashboardURL(yearID))
			return
		}
		if iv, err := s.store.GetInvoice(r.Context(), year.ID, neighborID); err == nil {
			msg := "Rechnung " + iv.Number + " ist festgeschrieben – Buchungen und Verrechnungen für diesen Nachbarn sind gesperrt. Für Korrekturen bitte die Rechnung stornieren."
			markPending(msg)
			reject(http.StatusUnprocessableEntity, msg, neighborURL(neighborID, yearID))
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			// A real store failure is transient — 500 so a replay retries instead of
			// dropping the rows as a permanent 422.
			s.serverError(w, r.URL.Path, err)
			return
		}
		created, paired, createErr = s.createQuickRows(r, year, neighborID, rows)
	}

	invalid := 0
	var reasons []string
	for _, row := range rows {
		if row.status == quickInvalid || row.status == quickConflict {
			invalid++
			reasons = append(reasons, fmt.Sprintf("Zeile %d: %s", row.index+1, row.message))
		}
	}
	// A store failure is transient, not a business rejection: answer 500 so a
	// replay retries later. Rows that did save carry their idempotency key, so the
	// retry adds only what is missing.
	if createErr != nil {
		s.serverError(w, "quick entries: create", createErr)
		return
	}
	// An offline replay gets an explicit status, never a redirect (see above).
	// 204 only for a fully-clean batch: invalid rows answer 422 with the honest
	// count, so the client surfaces the message instead of silently dropping a
	// day of captured work. The saved rows carry idempotency keys, so nothing
	// duplicates if the operator retries the retained batch with its original keys.
	if replay {
		if invalid == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		rejectRows(fmt.Sprintf("%d Buchung(en) + %d Mannstunden gespeichert, %d Zeile(n) ungültig (Gespann und Stunden erforderlich; eine gewählte Person braucht einen Stundensatz). %s",
			created, paired, invalid, strings.Join(reasons, " ")))
		return
	}
	switch {
	case invalid > 0:
		s.setFlash(w, r, "error", fmt.Sprintf("%d Buchung(en) + %d Mannstunden gespeichert, %d Zeile(n) übersprungen — Gespann und gültige Stunden erforderlich; eine gewählte Person braucht einen Stundensatz. %s",
			created, paired, invalid, strings.Join(reasons, " ")))
	case created == 0 && paired == 0 && already > 0:
		s.setFlash(w, r, "success", "Die Zeilen waren bereits erfasst.")
	case created == 0 && paired == 0:
		s.setFlash(w, r, "error", "Keine gültigen Zeilen (Gespann und Stunden erforderlich).")
	case created == 0 && paired > 0:
		// The machine halves were already recorded (a re-submit of a row that
		// carries its key), and only the Mannstunden were new. Saying "keine
		// gültigen Zeilen" here would deny a booking that just changed the
		// neighbor's balance.
		s.setFlash(w, r, "success", fmt.Sprintf("%d Mannstunden zu bereits erfassten Buchungen ergänzt.", paired))
	case paired > 0:
		s.setFlash(w, r, "success", fmt.Sprintf("%d Buchungen + %d Mannstunden gespeichert.", created, paired))
	default:
		s.setFlash(w, r, "success", fmt.Sprintf("%d Buchungen gespeichert.", created))
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// createQuickRows books every pending row. Invalid rows are marked on the row;
// a store failure stops the batch and is returned (rows already committed keep
// their audit, which is written in their own transaction).
func (s *Server) createQuickRows(r *http.Request, year *models.BillingYear, neighborID int64, rows []*quickRow) (created, paired int, createErr error) {
	adjustments, err := s.store.ListFuelAdjustments(r.Context(), year.Base.ID)
	if err != nil {
		return 0, 0, err
	}
	// The same rig repeats across the rows of one submit — that is what quick
	// entry is FOR — so each distinct Gespann is resolved once (5 queries) and
	// reused, instead of 5 queries per row (a 100-row submit was ~500 SELECTs).
	type resolvedRig struct {
		entry      models.Entry
		machineIDs []int64
		msg        string
	}
	rigs := map[int64]resolvedRig{}
	// Helpers repeat across the rows of one submit for the same reason rigs do
	// (one person, one afternoon, several fields), so each is resolved once —
	// including the negative answer, which must not re-query per row either.
	type resolvedPerson struct {
		person models.Person
		msg    string
	}
	persons := map[int64]resolvedPerson{}
	nb := s.neighborName(r, neighborID)
	for _, row := range rows {
		if row.status != "" {
			continue
		}
		rig, seen := rigs[row.gespannID]
		if !seen {
			e, mids, msg, err := s.buildGespannEntry(r, row.gespannID)
			if err == nil && msg == "" {
				// The rig must be priced from this year's basis (like the unified
				// form); a gespann from another Bemessungsgrundlage is refused.
				msg, err = s.checkBookingCatalog(r, e, mids, year.Base.ID, false)
			}
			if err != nil {
				// A store failure is transient, not a business rejection — and it
				// must not skip the rows already committed, whose audit is in place.
				return created, paired, err
			}
			rig = resolvedRig{machineIDs: mids, msg: msg}
			if msg == "" {
				rig.entry = *e
			}
			rigs[row.gespannID] = rig
		}
		if rig.msg != "" {
			row.status, row.message = quickInvalid, rig.msg
			continue
		}
		entry := rig.entry // copy of the resolved template
		applyEffectiveFuelAdjustment(&entry, store.EffectiveFuelAdjustment(adjustments, row.date))
		entry.Hours = row.hours
		entry.Cost = calc.Cost(row.hours, entry.HourlyRate)
		entry.Quantity, entry.UnitPrice = decimal.Decimal{}, decimal.Decimal{}
		entry.Date = row.date
		entry.NeighborID = neighborID
		entry.BillingYearID = year.ID
		entry.IdempotencyKey = row.key
		entry.RequestFingerprint = row.fingerprint
		audit := &store.EntryAudit{Action: "quick_create", Detail: fmt.Sprintf("%s · Schnellerfassung: %s, %s h × %s = %s €",
			nb, entry.TaskLabel, entry.Hours.StringFixed(2), entry.HourlyRate.StringFixed(2), entry.Cost.StringFixed(2))}
		var companion *models.Entry
		if row.personID != 0 {
			p, seen := persons[row.personID]
			if !seen {
				person, err := s.store.GetPerson(r.Context(), row.personID)
				switch {
				case err == nil && person.HourlyRate.IsPositive():
					p = resolvedPerson{person: *person}
				case err == nil:
					p = resolvedPerson{msg: "Für " + person.Name + " ist kein Stundensatz hinterlegt."}
				case errors.Is(err, store.ErrNotFound):
					p = resolvedPerson{msg: "Die gewählte Person ist nicht mehr vorhanden."}
				default:
					return created, paired, err
				}
				persons[row.personID] = p
			}
			if p.msg != "" {
				// The row named a helper the master data cannot price (or no
				// longer knows). Booking only the machine half would report
				// success while filing the work as unmanned, so the row is
				// skipped whole and counted like any other invalid row.
				row.status, row.message = quickInvalid, p.msg
				continue
			}
			personID := p.person.ID
			companion = &models.Entry{
				NeighborID: neighborID, BillingYearID: year.ID,
				Date: entry.Date, TaskLabel: "Mannstunden " + p.person.Name,
				Unit: unitMannstunde, Quantity: row.hours, UnitPrice: p.person.HourlyRate,
				Cost:     money.Amount(row.hours, p.person.HourlyRate),
				PersonID: &personID,
				// Derived from the row's own key, so a replayed row no-ops on
				// both halves (see models.CompanionKey).
				IdempotencyKey:     models.CompanionKey(entry.IdempotencyKey),
				RequestFingerprint: row.companionFingerprint,
			}
			audit.CompanionDetail = fmt.Sprintf("%s · Schnellerfassung: Mannstunden (verknüpft), %s h × %s = %s €",
				nb, companion.Quantity.String(), companion.UnitPrice.StringFixed(2), companion.Cost.StringFixed(2))
		}
		command := store.BookingCommand{
			Entry:      &entry,
			MachineIDs: rig.machineIDs,
			Audit:      audit,
		}
		if companion != nil {
			command.Helpers = []*models.Entry{companion}
		}
		result, err := s.store.CreateBooking(r.Context(), command)
		companionID := int64(0)
		if len(result.HelperIDs) == 1 {
			companionID = result.HelperIDs[0]
		}
		switch {
		case errors.Is(err, store.ErrIdempotencyConflict):
			row.status, row.message = quickConflict, replayConflictMsg
		case err != nil:
			// Joined, not last-wins: a partly failing batch should log every reason,
			// not just the reason the final row failed.
			createErr = errors.Join(createErr, err)
		default:
			row.status = quickSaved
			if result.MainID != 0 {
				created++
			}
			if companionID != 0 {
				paired++
			}
		}
	}
	return created, paired, createErr
}

// applyFuelAdjustment selects and snapshots the version effective on the
// booking date. No version is the historical zero-adjustment behavior.
func (s *Server) applyFuelAdjustment(ctx context.Context, baseID int64, date time.Time, entry *models.Entry) error {
	adjustment, err := s.store.FuelAdjustmentAt(ctx, baseID, date)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	applyEffectiveFuelAdjustment(entry, adjustment)
	return nil
}

// applyEffectiveFuelAdjustment adds one non-cumulative version to a catalog
// snapshot. A zero version explicitly switches the addition off.
func applyEffectiveFuelAdjustment(entry *models.Entry, adjustment *models.FuelAdjustment) {
	entry.FuelAdjustmentLabel = ""
	entry.FuelAdjustmentPerH = decimal.Zero
	if adjustment == nil || !adjustment.AmountPerH.IsPositive() {
		return
	}
	entry.FuelAdjustmentLabel = adjustment.Label
	entry.FuelAdjustmentPerH = adjustment.AmountPerH
	entry.HourlyRate = entry.HourlyRate.Add(adjustment.AmountPerH).Round(2)
}

// buildGespannEntry resolves a fixed gespann into a snapshotted entry template
// (rate and labels; hours, date and account are set by the caller). A missing
// or incomplete rig is a validation message; any other store failure is an
// error, so a replay retries instead of rejecting the rows for good.
func (s *Server) buildGespannEntry(r *http.Request, gespannID int64) (*models.Entry, []int64, string, error) {
	const gone = "Das gewählte Gespann ist nicht mehr vorhanden."
	resolved, failure, err := s.resolveEquipment(r.Context(), equipmentSelection{GespannID: &gespannID})
	if err != nil {
		return nil, nil, "", err
	}
	switch failure {
	case equipmentResolutionIncompletePair, equipmentResolutionEmpty:
		return nil, nil, "Das Gespann „" + resolved.TaskLabel + "“ ist unvollständig — bitte in den Gespannen ergänzen.", nil
	case equipmentResolutionGespannMissing, equipmentResolutionTractorMissing,
		equipmentResolutionLoadMissing, equipmentResolutionMachineMissing:
		return nil, nil, gone, nil
	}
	entry, ids := resolved.entrySnapshot()
	return &entry, ids, "", nil
}

// handleEntryDelete removes a booking only while its year remains open.
// A linked companion is deleted with it only when the form explicitly requests
// cascade deletion; successful deletions are audited before returning to the account.
func (s *Server) handleEntryDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if !s.entryYearOpen(w, r, entry, "Das Abrechnungsjahr ist abgeschlossen – Buchungen können nicht mehr gelöscht werden.") {
		return
	}
	// Linked pair (person booked with the Gespann): the confirm dialog offered a
	// "delete both" checkbox; cascade only on that explicit choice.
	partnerID := int64(0)
	if r.FormValue("cascade") == "1" {
		if pid, err := s.store.LinkedPartnerID(r.Context(), id); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		} else {
			partnerID = pid
		}
	}
	nb := s.neighborName(r, entry.NeighborID)
	if partnerID != 0 {
		partner, perr := s.store.GetEntry(r.Context(), partnerID)
		if err := s.store.DeleteEntryPair(r.Context(), id, partnerID); err != nil {
			s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
		} else {
			s.audit(r, "delete", "entry", id, fmt.Sprintf("%s · %s, %s h, %s € (verknüpftes Paar)",
				nb, entry.TractorLabel, entry.Hours.StringFixed(2), entry.Cost.StringFixed(2)))
			if perr == nil {
				s.audit(r, "delete", "entry", partnerID, fmt.Sprintf("%s · %s, %s € (verknüpftes Paar)",
					nb, partner.TaskLabel, partner.Cost.StringFixed(2)))
			}
			s.setFlash(w, r, "success", "Buchung und verknüpfte Buchung gelöscht.")
		}
		redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
		return
	}
	if err := s.store.DeleteEntry(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "delete", "entry", id, fmt.Sprintf("%s · %s, %s h, %s €",
			nb, entry.TractorLabel,
			entry.Hours.StringFixed(2), entry.Cost.StringFixed(2)))
		s.setFlash(w, r, "success", "Buchung gelöscht.")
	}
	redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
}
