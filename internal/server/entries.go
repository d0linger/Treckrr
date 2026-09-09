package server

import (
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
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
)

func (s *Server) handleNeighborDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	base := year.Base

	entries, err := s.store.ListEntries(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	cost, hours, err := s.store.NeighborTotal(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	// Bidirectional ledger: manual postings that net against the bookings.
	ledger, err := s.store.ListNeighborLedger(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	ledgerSum := decimal.Zero
	for _, l := range ledger {
		if !l.Voided {
			ledgerSum = ledgerSum.Add(l.Amount)
		}
	}

	// Payments toward this year (dated amounts). The remaining balance is the
	// saldo minus what was paid; payments are editable regardless of year status.
	payments, err := s.store.ListPayments(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	paidSum := decimal.Zero
	for _, p := range payments {
		paidSum = paidSum.Add(p.Amount)
	}

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
	data["Stale"] = stale
	data["StaleCount"] = len(stale)
	data["TaskSummary"] = summarizeByTask(entries)
	data["Completed"] = year.Completed()
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Base"] = base
	data["Neighbor"] = neighbor
	data["Entries"] = entries
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
	data["LinkedFrom"] = linkedFrom
	data["PairLabel"] = pairLabel
	data["TotalCost"] = cost
	data["TotalHours"] = hours
	data["Ledger"] = ledger
	data["LedgerSum"] = ledgerSum
	data["Saldo"] = cost.Add(ledgerSum)
	data["Payments"] = payments
	data["PaidSum"] = paidSum
	plans, err := s.store.ListInstallments(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data["Installments"] = installmentViews(plans, paidSum)
	remaining := cost.Add(ledgerSum).Sub(paidSum)
	data["Remaining"] = remaining
	// The credit shown on the payout/carry buttons: the negative rest, made
	// positive for display ("Guthaben (45,00 €)").
	data["CreditAmount"] = remaining.Neg()
	// An issued invoice enables the Skonto (§16) option on the payment form.
	_, invErr := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	data["HasInvoice"] = invErr == nil
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
	data["PhotoCounts"] = photoCounts
	data["Photos"] = photos
	data["Persons"] = persons
	data["TravelFlat"] = company.TravelFlat
	data["TravelPerKm"] = company.TravelPerKm
	data["HasTravelRates"] = company.TravelFlat.IsPositive() || company.TravelPerKm.IsPositive()
	data["Tractors"] = tractors
	data["Loads"] = loads
	data["Machines"] = machines
	data["Gespanne"] = gespanne
	data["Today"] = time.Now().Format("2006-01-02")
	s.render(w, r, "neighbor", data)
}

// handleNeighborOverview shows one neighbor across all billing years with cost,
// hours and payment status (payment history).
func (s *Server) handleNeighborOverview(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
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
		Paid      bool
		Completed bool
	}
	rows := make([]yearRow, 0, len(history))
	var totalCost, totalHours decimal.Decimal
	for _, h := range history {
		rows = append(rows, yearRow{
			Year: h.Year, YearID: h.YearID, Cost: h.Net, Hours: h.Hours,
			Paid: h.Paid, Completed: h.Status == models.YearCompleted,
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
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
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
		sd.Rows = append(sd.Rows, pdf.StatementYear{Year: h.Year, Cost: h.Net, Hours: h.Hours, Paid: h.Paid})
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
		http.NotFound(w, r)
		return
	}
	tractors, _ := s.store.ListActiveTractors(r.Context(), id)
	loads, _ := s.store.ListLoadLevels(r.Context(), id)
	machines, _ := s.store.ListActiveMachines(r.Context(), id)
	gespanne, _ := s.store.ListGespanne(r.Context(), id)

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
	out := struct {
		Tractors []apiTractor `json:"tractors"`
		Loads    []apiLoad    `json:"loads"`
		Machines []apiMachine `json:"machines"`
		Gespanne []apiGespann `json:"gespanne"`
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
	neighborID := formInt64(r, "neighbor_id")
	yearID := formInt64(r, "year_id")
	// An offline replay (offline.js) sets this header and wants a machine-readable
	// status, not a redirect: 2xx = stored, 422 = permanent business rejection so
	// the client drops it from the queue (and 401 from auth = retry after login).
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
		// offline replay retries rather than dropping the booking as a permanent 422.
		s.serverError(w, r.URL.Path, err)
		return
	}

	entry, machineIDs, msg := s.resolveEntryFromForm(r)
	if msg != "" {
		reject(http.StatusUnprocessableEntity, msg, neighborURL(neighborID, yearID))
		return
	}
	idempotencyKey := trimmed(r, "idempotency_key") // set only for offline replays
	if s.tooLong(w, r, "Idempotency-Key", idempotencyKey, maxNameLen) {
		reject(http.StatusUnprocessableEntity, "Idempotency-Key darf höchstens 100 Zeichen lang sein.", neighborURL(neighborID, yearID))
		return
	}
	entry.NeighborID = neighborID
	entry.BillingYearID = year.ID
	entry.IdempotencyKey = idempotencyKey

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
			reject(http.StatusUnprocessableEntity, "Für "+person.Name+" ist kein Stundensatz hinterlegt — bitte im Personenstamm ergänzen.", neighborURL(neighborID, yearID))
			return
		}
		companion = &models.Entry{
			NeighborID: neighborID, BillingYearID: year.ID,
			Date: entry.Date, TaskLabel: "Mannstunden " + person.Name,
			Unit: unitMannstunde, Quantity: entry.Hours, UnitPrice: person.HourlyRate,
			Cost:     entry.Hours.Mul(person.HourlyRate).Round(2),
			PersonID: &person.ID,
			// Derived, deterministic key so a replayed pair no-ops on both halves.
			// The base key is capped at maxNameLen upstream; trim before the
			// suffix so the derived key stays inside the column's contract.
			IdempotencyKey: companionKey(idempotencyKey),
		}
	}

	var newID, companionID int64
	if companion != nil {
		newID, companionID, err = s.store.CreateEntryPair(r.Context(), entry, machineIDs, companion)
	} else {
		newID, err = s.store.CreateEntry(r.Context(), entry, machineIDs)
	}
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if companionID != 0 {
		s.audit(r, "create", "entry", companionID, fmt.Sprintf("%s · Mannstunden (verknüpft), %s h × %s = %s €",
			s.neighborName(r, neighborID), companion.Quantity.String(),
			companion.UnitPrice.StringFixed(2), companion.Cost.StringFixed(2)))
	}
	if newID == 0 { // duplicate replay of an offline booking — already recorded
		if replay {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.setFlash(w, r, "success", "Buchung war bereits erfasst.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	var detail string
	if entry.Unit != "" && entry.Unit != "h" {
		detail = fmt.Sprintf("%s · %s, %s %s × %s = %s €",
			s.neighborName(r, neighborID), entry.TaskLabel,
			entry.Quantity.String(), entry.Unit, entry.UnitPrice.StringFixed(2), entry.Cost.StringFixed(2))
	} else {
		detail = fmt.Sprintf("%s · %s, %s h × %s = %s €",
			s.neighborName(r, neighborID), entry.TaskLabel,
			entry.Hours.StringFixed(2), entry.HourlyRate.StringFixed(2), entry.Cost.StringFixed(2))
	}
	s.audit(r, "create", "entry", newID, detail)
	if replay {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if companionID != 0 {
		s.setFlash(w, r, "success", "Buchung + Mannstunden gespeichert.")
	} else {
		s.setFlash(w, r, "success", "Buchung gespeichert.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// companionKey derives the companion entry's idempotency key from the machine
// entry's key ("" stays "" — online submits carry no key). Deterministic, so a
// replayed pair resolves to the same two keys; trimmed so the suffix never
// pushes a maxNameLen-length base key over the column's contract.
func companionKey(base string) string {
	if base == "" {
		return ""
	}
	if len(base) > maxNameLen-2 {
		base = base[:maxNameLen-2]
	}
	return base + "-p"
}

// resolveEntryFromForm reads the booking form fields, resolves the tractor,
// load level and machines (from a fixed gespann or manual selection) and
// returns a populated Entry (without neighbor/year) plus its machine ids. On
// validation failure it returns a non-empty German message.
func (s *Server) resolveEntryFromForm(r *http.Request) (*models.Entry, []int64, string) {
	// Non-hour unit (ha, Ballen, m³, …): quantity × unit price, no rig required.
	// Hours stay 0 (they don't count toward TotalHours). Unit "h" (or empty) falls
	// through to the rig-based hourly path below. "__custom" resolves to the
	// free-text unit field.
	unit := trimmed(r, "unit")
	if unit == "__custom" {
		unit = trimmed(r, "unit_custom")
		if unit == "" {
			return nil, nil, "Bitte eine eigene Einheit angeben."
		}
	}
	if unit != "" && unit != "h" {
		if msg := lenError("Einheit", unit, 16); msg != "" {
			return nil, nil, msg
		}
		taskLabel := trimmed(r, "task_label")
		if taskLabel == "" {
			return nil, nil, "Bitte eine Tätigkeit angeben."
		}
		if msg := lenError("Tätigkeit", taskLabel, maxNameLen); msg != "" {
			return nil, nil, msg
		}
		quantity := formDecimal(r, "quantity")
		if !quantity.IsPositive() {
			return nil, nil, "Menge muss größer als 0 sein."
		}
		unitPrice := formDecimal(r, "unit_price")
		if !unitPrice.IsPositive() {
			return nil, nil, "Preis je Einheit muss größer als 0 sein."
		}
		note := trimmed(r, "note")
		if msg := lenError("Notiz", note, maxNoteLen); msg != "" {
			return nil, nil, msg
		}
		entryDate, err := time.Parse("2006-01-02", trimmed(r, "entry_date"))
		if err != nil {
			entryDate = time.Now()
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
		}, nil, ""
	}

	machineIDs, ok := formMachineIDs(r)
	if !ok {
		return nil, nil, "Zu viele Maschinen auf einmal."
	}
	var (
		gespannID   *int64
		tractorID   = formInt64Ptr(r, "tractor_id")
		loadLevelID = formInt64Ptr(r, "load_level_id")
		taskLabel   = trimmed(r, "task_label")
	)
	if r.FormValue("mode") != "manual" {
		if gid := formInt64(r, "gespann_id"); gid != 0 {
			g, err := s.store.GetGespann(r.Context(), gid)
			if err == nil {
				gespannID = &g.ID
				tractorID = g.TractorID
				loadLevelID = g.LoadLevelID
				machineIDs = g.MachineIDs
				if taskLabel == "" {
					taskLabel = g.Name
				}
			}
		} else {
			tractorID, loadLevelID = nil, nil
		}
	}
	// The tractor is optional — a booking may be machines only, for work where the
	// customer supplies the tractor — but the pair is all-or-nothing, because
	// TractorRate needs both to produce a number.
	if (tractorID == nil) != (loadLevelID == nil) {
		return nil, nil, "Traktor und Belastungsstufe gehören zusammen — bitte beides wählen oder beides leer lassen."
	}
	if tractorID == nil && len(machineIDs) == 0 {
		return nil, nil, "Bitte Traktor und Belastungsstufe oder mindestens eine Maschine wählen."
	}
	var tractor *models.Tractor
	var load *models.LoadLevel
	if tractorID != nil {
		t, err := s.store.GetTractor(r.Context(), *tractorID)
		if err != nil {
			return nil, nil, "Traktor nicht gefunden."
		}
		l, err := s.store.GetLoadLevel(r.Context(), *loadLevelID)
		if err != nil {
			return nil, nil, "Belastungsstufe nicht gefunden."
		}
		tractor, load = t, l
	}
	machines, err := s.store.MachinesByIDs(r.Context(), machineIDs)
	if err != nil {
		return nil, nil, "Interner Fehler beim Laden der Maschinen."
	}
	// Every submitted machine id must resolve, tractor or not. The machines-only
	// case priced at 0,00 € (reproduced: 3 h at 0,0000, "gespeichert"); WITH a
	// tractor the silent drop was subtler and worse — the booking saved at the
	// bare tractor rate, underbilling by the vanished machine's share without
	// anyone noticing. A stale form after a machine was deleted is exactly when
	// the user must be told, not accommodated (Ausbaukarte Nr. 61).
	if len(machines) != len(machineIDs) {
		return nil, nil, "Die gewählten Maschinen sind nicht mehr verfügbar — bitte die Seite neu laden."
	}
	hours := formDecimal(r, "hours")
	if !hours.IsPositive() {
		return nil, nil, "Stunden müssen größer als 0 sein."
	}
	entryDate, err := time.Parse("2006-01-02", trimmed(r, "entry_date"))
	if err != nil {
		entryDate = time.Now()
	}
	if msg := lenError("Tätigkeit", taskLabel, maxNameLen); msg != "" {
		return nil, nil, msg
	}
	note := trimmed(r, "note")
	if msg := lenError("Notiz", note, maxNoteLen); msg != "" {
		return nil, nil, msg
	}
	rate := calc.GespannRate(tractor, load, machines)
	names := make([]string, 0, len(machines))
	ids := make([]int64, 0, len(machines))
	for _, m := range machines {
		names = append(names, m.Name)
		ids = append(ids, m.ID)
	}
	entry := &models.Entry{
		Date:          entryDate,
		TaskLabel:     taskLabel,
		GespannID:     gespannID,
		MachineLabels: strings.Join(names, ", "),
		Hours:         hours,
		HourlyRate:    rate,
		Cost:          calc.Cost(hours, rate),
		Note:          note,
	}
	// Left nil/empty on a machines-only booking, which is what the templates and
	// the recalculation branch on to tell the two shapes apart.
	if tractor != nil && load != nil {
		entry.TractorID, entry.LoadLevelID = &tractor.ID, &load.ID
		entry.TractorLabel, entry.LoadLabel = tractor.Label(), load.Name
	}
	return entry, ids, ""
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
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	existing, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.entryYearOpen(w, r, existing, "Das Abrechnungsjahr ist abgeschlossen – Buchungen können nicht mehr geändert werden.") {
		return
	}
	entry, machineIDs, msg := s.resolveEntryFromForm(r)
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
		return
	}
	entry.ID = id
	if err := s.store.UpdateEntry(r.Context(), entry, machineIDs); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "update", "entry", id, s.neighborName(r, existing.NeighborID)+" · "+entryUpdateDetail(existing, entry))
	// Linked pair: mirror the edited hours onto the partner when the edit form's
	// checkbox asked for it — machine and Mannstunden of one Einsatz share the
	// same hours, each priced at its own frozen rate.
	if r.FormValue("sync_pair") == "1" {
		hours := entry.Hours
		if entry.Unit != "" && entry.Unit != "h" {
			hours = entry.Quantity
		}
		if pid, err := s.store.LinkedPartnerID(r.Context(), id); err != nil {
			s.serverError(w, r.URL.Path, err)
			return
		} else if pid != 0 && hours.IsPositive() {
			cost, err := s.store.SyncPairHours(r.Context(), pid, hours)
			if err != nil {
				s.setFlash(w, r, "error", "Buchung aktualisiert, aber die verknüpfte Buchung konnte nicht angepasst werden.")
				redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
				return
			}
			s.audit(r, "update", "entry", pid, fmt.Sprintf("%s · verknüpft angeglichen: %s h, %s €",
				s.neighborName(r, existing.NeighborID), hours.String(), cost.StringFixed(2)))
			s.setFlash(w, r, "success", "Buchung und verknüpfte Buchung aktualisiert.")
			redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
			return
		}
	}
	s.setFlash(w, r, "success", "Buchung aktualisiert.")
	redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
}

// handleEntryVoid cancels or restores a booking (traceable alternative to
// deletion). Voided bookings are excluded from totals.
func (s *Server) handleEntryVoid(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
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
		s.audit(r, "ledger_add", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID)+" · "+amount.StringFixed(2)+" € "+description)
		s.setFlash(w, r, "success", "Position hinzugefügt.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handleLedgerEditForm renders the edit form for one posting.
func (s *Server) handleLedgerEditForm(w http.ResponseWriter, r *http.Request) {
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
	data := s.newPage(w, r, "Position bearbeiten", "dashboard")
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Ledger"] = e
	data["IsCredit"] = e.Amount.IsNegative()
	data["AbsAmount"] = e.Amount.Abs()
	s.render(w, r, "ledger_edit", data)
}

// handleLedgerUpdate saves an edited posting (while the year is open).
func (s *Server) handleLedgerUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID, neighborID, _, err := s.store.GetLedgerEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !s.ledgerYearOpen(w, r, yearID, neighborID) {
		return
	}
	amount, description, date, msg := ledgerFormValues(r)
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.UpdateNeighborLedger(r.Context(), id, amount, description, date); err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen.")
	} else {
		s.audit(r, "ledger_update", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · "+amount.StringFixed(2)+" € "+description)
		s.setFlash(w, r, "success", "Position aktualisiert.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handleLedgerVoid cancels or restores a posting (traceable alternative to
// deletion; a voided posting stays visible but is excluded from the balance).
func (s *Server) handleLedgerVoid(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID, neighborID, e, err := s.store.GetLedgerEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
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
			s.audit(r, "ledger_void", "neighbor", neighborID, s.neighborName(r, neighborID)+" · Übertrag storniert (beide Seiten)")
			s.setFlash(w, r, "success", "Übertrag storniert — beide Seiten aufgehoben.")
		} else {
			s.audit(r, "ledger_unvoid", "neighbor", neighborID, s.neighborName(r, neighborID)+" · Übertrag wiederhergestellt")
			s.setFlash(w, r, "success", "Übertrags-Stornierung aufgehoben.")
		}
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.SetLedgerVoided(r.Context(), id, void, reason); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if void {
		s.audit(r, "ledger_void", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · "+e.Amount.StringFixed(2)+" € "+reason)
		s.setFlash(w, r, "success", "Position storniert.")
	} else {
		s.audit(r, "ledger_unvoid", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · "+e.Amount.StringFixed(2)+" €")
		s.setFlash(w, r, "success", "Stornierung aufgehoben.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// handleLedgerDelete removes a manual posting (while the year is open).
func (s *Server) handleLedgerDelete(w http.ResponseWriter, r *http.Request) {
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
			s.audit(r, "ledger_delete", "neighbor", neighborID, s.neighborName(r, neighborID)+" · Übertrag rückgängig")
			s.setFlash(w, r, "success", "Übertrag rückgängig gemacht — der Rest ist im anderen Jahr wieder offen.")
		}
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	if err := s.store.DeleteNeighborLedger(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "ledger_delete", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · "+e.Amount.StringFixed(2)+" € "+e.Description)
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
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), entry.NeighborID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), entry.BillingYearID)
	if err != nil {
		http.NotFound(w, r)
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
	data["NextWeek"] = time.Now().AddDate(0, 0, 7).Format("2006-01-02")
	// Linked pair: name the partner so the form can offer to mirror edited hours.
	if pid, err := s.store.LinkedPartnerID(r.Context(), id); err == nil && pid != 0 {
		if partner, err := s.store.GetEntry(r.Context(), pid); err == nil {
			label := partner.TaskLabel
			if label == "" {
				label = "verknüpfte Buchung"
			}
			data["PairPartnerLabel"] = label
		}
	}
	s.render(w, r, "entry_edit", data)
}

// handleEntryCopy renders the entry form pre-filled from an existing booking as a
// NEW booking (dated today), so a recurring task can be re-entered in one click.
// It reuses the entry_edit template with Copy=true, which points the form at the
// create route and adds the neighbor/year hidden fields.
func (s *Server) handleEntryCopy(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), entry.NeighborID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), entry.BillingYearID)
	if err != nil {
		http.NotFound(w, r)
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
	data["Copy"] = true
	s.render(w, r, "entry_edit", data)
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
	reject := func(status int, msg, redirectTo string) {
		if replay {
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
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if errors.Is(err, store.ErrNotFound) {
		s.badRequest(w, "Unbekanntes Abrechnungsjahr")
		return
	} else if err != nil {
		s.serverError(w, "quick entries: load year", err)
		return
	}
	if year.Completed() {
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
		reject(http.StatusUnprocessableEntity, "Nachbar ist diesem Abrechnungsjahr nicht zugeordnet.", dashboardURL(yearID))
		return
	}
	if iv, err := s.store.GetInvoice(r.Context(), year.ID, neighborID); err == nil {
		reject(http.StatusUnprocessableEntity, "Rechnung "+iv.Number+" ist festgeschrieben – Buchungen und Verrechnungen für diesen Nachbarn sind gesperrt. Für Korrekturen bitte die Rechnung stornieren.", neighborURL(neighborID, yearID))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		// A real store failure is transient — 500 so a replay retries instead of
		// dropping the rows as a permanent 422.
		s.serverError(w, r.URL.Path, err)
		return
	}

	dates := r.Form["q_date"]
	gespanne := r.Form["q_gespann"]
	hoursList := r.Form["q_hours"]
	// Offline replay (Ausbaukarte 100): the client sends one key PER ROW, since
	// one submit becomes N bookings and a single key would let a replay create
	// only the first of them. Absent for an online submit, which keeps the
	// previous behavior exactly.
	keys := r.Form["q_key"]
	created := 0
	var createErr error
	for i := range gespanne {
		gid, _ := strconv.ParseInt(strings.TrimSpace(gespanne[i]), 10, 64)
		hours := decimal.Zero
		if i < len(hoursList) {
			hours = parseGermanDecimal(hoursList[i])
		}
		if gid == 0 || !hours.IsPositive() {
			continue
		}
		dateStr := ""
		if i < len(dates) {
			dateStr = strings.TrimSpace(dates[i])
		}
		entry, machineIDs, ok := s.buildGespannEntry(r, gid, hours, dateStr)
		if !ok {
			continue
		}
		entry.NeighborID = neighborID
		entry.BillingYearID = year.ID
		if i < len(keys) {
			key := strings.TrimSpace(keys[i])
			if len(key) <= maxNameLen {
				entry.IdempotencyKey = key
			}
		}
		id, err := s.store.CreateEntry(r.Context(), entry, machineIDs)
		switch {
		case err != nil:
			// Joined, not last-wins: a partly failing batch should log every reason,
			// not just the reason the final row failed.
			createErr = errors.Join(createErr, err)
		case id != 0:
			created++
		}
	}
	// Audit before answering, replay included: a replayed batch is a booking like
	// any other, and its § 132 BAO trail must not depend on how it reached us.
	if created > 0 {
		s.audit(r, "quick_create", "entry", 0, fmt.Sprintf("%d Buchungen für %s", created, s.neighborName(r, neighborID)))
	}
	// A store failure is transient, not a business rejection: answer 500 so a
	// replay retries later. Rows that did save carry their idempotency key, so the
	// retry adds only what is missing.
	if createErr != nil {
		s.serverError(w, "quick entries: create", createErr)
		return
	}
	// An offline replay gets an explicit status, never a redirect (see above).
	// 204 only for rows that actually landed: answering it for a batch where every
	// row was rejected would make the client delete the queue item, and a day of
	// captured work would be gone with no flash, no audit line and no log entry.
	if replay {
		if created == 0 {
			http.Error(w, "Keine gültigen Zeilen (Gespann und Stunden erforderlich).", http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if created == 0 {
		s.setFlash(w, r, "error", "Keine gültigen Zeilen (Gespann und Stunden erforderlich).")
	} else {
		s.setFlash(w, r, "success", fmt.Sprintf("%d Buchungen gespeichert.", created))
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// buildGespannEntry resolves a fixed gespann into a snapshotted entry.
func (s *Server) buildGespannEntry(r *http.Request, gespannID int64, hours decimal.Decimal, dateStr string) (*models.Entry, []int64, bool) {
	g, err := s.store.GetGespann(r.Context(), gespannID)
	// A machines-only rig is valid; a half-set tractor pair is not (see
	// calc.GespannRate), and neither is a rig with nothing in it at all.
	if err != nil || (g.TractorID == nil) != (g.LoadLevelID == nil) {
		return nil, nil, false
	}
	if g.TractorID == nil && len(g.MachineIDs) == 0 {
		return nil, nil, false
	}
	var tractor *models.Tractor
	var load *models.LoadLevel
	if g.TractorID != nil {
		tractor, err = s.store.GetTractor(r.Context(), *g.TractorID)
		if err != nil {
			return nil, nil, false
		}
		load, err = s.store.GetLoadLevel(r.Context(), *g.LoadLevelID)
		if err != nil {
			return nil, nil, false
		}
	}
	machines, err := s.store.MachinesByIDs(r.Context(), g.MachineIDs)
	if err != nil {
		return nil, nil, false
	}
	// See buildEntryFromForm: every stored machine id must resolve, or the row is
	// priced without the missing machine's share and quietly underbills.
	if len(machines) != len(g.MachineIDs) {
		return nil, nil, false
	}
	entryDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		entryDate = time.Now()
	}
	rate := calc.GespannRate(tractor, load, machines)
	names := make([]string, 0, len(machines))
	ids := make([]int64, 0, len(machines))
	for _, m := range machines {
		names = append(names, m.Name)
		ids = append(ids, m.ID)
	}
	gid := g.ID
	entry := &models.Entry{
		Date:          entryDate,
		TaskLabel:     g.Name,
		GespannID:     &gid,
		MachineLabels: strings.Join(names, ", "),
		Hours:         hours,
		HourlyRate:    rate,
		Cost:          calc.Cost(hours, rate),
	}
	if tractor != nil && load != nil {
		entry.TractorID, entry.LoadLevelID = &tractor.ID, &load.ID
		entry.TractorLabel, entry.LoadLabel = tractor.Label(), load.Name
	}
	return entry, ids, true
}

func (s *Server) handleEntryDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
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
