package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
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
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	cost, hours, err := s.store.NeighborTotal(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	// Bidirectional ledger: manual postings that net against the bookings.
	ledger, err := s.store.ListNeighborLedger(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	ledgerSum := decimal.Zero
	for _, l := range ledger {
		if !l.Voided {
			ledgerSum = ledgerSum.Add(l.Amount)
		}
	}

	// Only active tractors/machines can be booked; inactive ones remain only
	// on historical entries.
	tractors, _ := s.store.ListActiveTractors(r.Context(), base.ID)
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	machines, _ := s.store.ListActiveMachines(r.Context(), base.ID)
	gespanne, _ := s.store.ListGespanne(r.Context(), base.ID)

	data := s.newPage(w, r, neighbor.Name, "dashboard")
	data["TaskSummary"] = summarizeByTask(entries)
	data["Completed"] = year.Completed()
	if err := s.withYearSelector(r, data, year); err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	data["Base"] = base
	data["Neighbor"] = neighbor
	data["Entries"] = entries
	data["TotalCost"] = cost
	data["TotalHours"] = hours
	data["Ledger"] = ledger
	data["LedgerSum"] = ledgerSum
	data["Saldo"] = cost.Add(ledgerSum)
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
	years, err := s.store.ListBillingYears(r.Context())
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
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
	var rows []yearRow
	var totalCost, totalHours decimal.Decimal
	for _, y := range years {
		member, err := s.store.NeighborInYear(r.Context(), y.ID, id)
		if err != nil || !member {
			continue
		}
		cost, hours, _ := s.store.NeighborTotal(r.Context(), id, y.ID)
		payments, _ := s.store.YearPayments(r.Context(), y.ID)
		rows = append(rows, yearRow{
			Year: y.Year, YearID: y.ID, Cost: cost, Hours: hours,
			Paid: payments[id], Completed: y.Completed(),
		})
		totalCost = totalCost.Add(cost)
		totalHours = totalHours.Add(hours)
	}
	data := s.newPage(w, r, neighbor.Name+" · Verlauf", "dashboard")
	data["Neighbor"] = neighbor
	data["Rows"] = rows
	data["TotalCost"] = totalCost
	data["TotalHours"] = totalHours
	s.render(w, r, "neighbor_overview", data)
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

func (s *Server) handleEntryCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	neighborID := formInt64(r, "neighbor_id")
	yearID := formInt64(r, "year_id")

	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.Error(w, "Unbekanntes Abrechnungsjahr", http.StatusBadRequest)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen – es können keine Buchungen mehr erfasst werden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}

	entry, machineIDs, msg := s.resolveEntryFromForm(r)
	if msg != "" {
		s.setFlash(w, r, "error", msg)
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	entry.NeighborID = neighborID
	entry.BillingYearID = year.ID

	newID, err := s.store.CreateEntry(r.Context(), entry, machineIDs)
	if err != nil {
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.audit(r, "create", "entry", newID, fmt.Sprintf("%s · %s, %s h × %s = %s €",
		s.neighborName(r, neighborID), entry.TaskLabel,
		entry.Hours.StringFixed(2), entry.HourlyRate.StringFixed(2), entry.Cost.StringFixed(2)))
	s.setFlash(w, r, "success", "Buchung gespeichert.")
	redirect(w, r, neighborURL(neighborID, yearID))
}

// resolveEntryFromForm reads the booking form fields, resolves the tractor,
// load level and machines (from a fixed gespann or manual selection) and
// returns a populated Entry (without neighbor/year) plus its machine ids. On
// validation failure it returns a non-empty German message.
func (s *Server) resolveEntryFromForm(r *http.Request) (*models.Entry, []int64, string) {
	var (
		gespannID   *int64
		tractorID   = formInt64Ptr(r, "tractor_id")
		loadLevelID = formInt64Ptr(r, "load_level_id")
		machineIDs  = formMachineIDs(r)
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
	if tractorID == nil || loadLevelID == nil {
		return nil, nil, "Bitte Traktor und Belastungsstufe (oder ein Gespann) wählen."
	}
	tractor, err := s.store.GetTractor(r.Context(), *tractorID)
	if err != nil {
		return nil, nil, "Traktor nicht gefunden."
	}
	load, err := s.store.GetLoadLevel(r.Context(), *loadLevelID)
	if err != nil {
		return nil, nil, "Belastungsstufe nicht gefunden."
	}
	machines, err := s.store.MachinesByIDs(r.Context(), machineIDs)
	if err != nil {
		return nil, nil, "Interner Fehler beim Laden der Maschinen."
	}
	hours := formDecimal(r, "hours")
	if !hours.IsPositive() {
		return nil, nil, "Stunden müssen größer als 0 sein."
	}
	entryDate, err := time.Parse("2006-01-02", trimmed(r, "entry_date"))
	if err != nil {
		entryDate = time.Now()
	}
	rate := calc.GespannRate(*tractor, *load, machines)
	names := make([]string, 0, len(machines))
	ids := make([]int64, 0, len(machines))
	for _, m := range machines {
		names = append(names, m.Name)
		ids = append(ids, m.ID)
	}
	return &models.Entry{
		Date:          entryDate,
		TaskLabel:     taskLabel,
		GespannID:     gespannID,
		TractorID:     &tractor.ID,
		LoadLevelID:   &load.ID,
		TractorLabel:  tractor.Label(),
		LoadLabel:     load.Name,
		MachineLabels: strings.Join(names, ", "),
		Hours:         hours,
		HourlyRate:    rate,
		Cost:          calc.Cost(hours, rate),
		Note:          trimmed(r, "note"),
	}, ids, ""
}

// entryUpdateDetail renders a per-field old→new summary of an edited booking so
// the audit trail shows what actually changed, not just the resulting cost.
func entryUpdateDetail(prev, cur *models.Entry) string {
	d := diffFields(
		fieldChange{"Datum", prev.Date.Format("02.01.2006"), cur.Date.Format("02.01.2006")},
		fieldChange{"Tätigkeit", prev.TaskLabel, cur.TaskLabel},
		fieldChange{"Maschinen", prev.MachineLabels, cur.MachineLabels},
		fieldChange{"Stunden", prev.Hours.StringFixed(2), cur.Hours.StringFixed(2)},
		fieldChange{"Satz", prev.HourlyRate.StringFixed(2), cur.HourlyRate.StringFixed(2)},
		fieldChange{"Kosten", prev.Cost.StringFixed(2) + " €", cur.Cost.StringFixed(2) + " €"},
		fieldChange{"Notiz", prev.Note, cur.Note},
	)
	if d == "" {
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	existing, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if year, err := s.store.GetBillingYear(r.Context(), existing.BillingYearID); err == nil && year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen – Buchungen können nicht mehr geändert werden.")
		redirect(w, r, neighborURL(existing.NeighborID, existing.BillingYearID))
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
		http.Error(w, "Interner Fehler", http.StatusInternalServerError)
		return
	}
	s.audit(r, "update", "entry", id, entryUpdateDetail(existing, entry))
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	entry, err := s.store.GetEntry(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if year, err := s.store.GetBillingYear(r.Context(), entry.BillingYearID); err == nil && year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
		return
	}
	void := r.FormValue("voided") == "true"
	reason := trimmed(r, "reason")
	nb := s.neighborName(r, entry.NeighborID)
	if err := s.store.SetEntryVoided(r.Context(), id, void, reason); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if void {
		s.audit(r, "void", "entry", id, fmt.Sprintf("%s · %s € %s", nb, entry.Cost.StringFixed(2), reason))
		s.setFlash(w, r, "success", "Buchung storniert.")
	} else {
		s.audit(r, "unvoid", "entry", id, nb)
		s.setFlash(w, r, "success", "Stornierung aufgehoben.")
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
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
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
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
	if err := s.store.DeleteNeighborLedger(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "ledger_delete", "neighbor", neighborID,
			s.neighborName(r, neighborID)+" · "+e.Amount.StringFixed(2)+" € "+e.Description)
		s.setFlash(w, r, "success", "Position entfernt.")
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

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
	s.render(w, r, "entry_edit", data)
}

// handleQuickEntries creates several bookings at once from the quick-entry rows
// (date, fixed gespann, hours).
func (s *Server) handleQuickEntries(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	neighborID := formInt64(r, "neighbor_id")
	yearID := formInt64(r, "year_id")
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.Error(w, "Unbekanntes Abrechnungsjahr", http.StatusBadRequest)
		return
	}
	if year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}

	dates := r.Form["q_date"]
	gespanne := r.Form["q_gespann"]
	hoursList := r.Form["q_hours"]
	created := 0
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
		if _, err := s.store.CreateEntry(r.Context(), entry, machineIDs); err == nil {
			created++
		}
	}
	if created == 0 {
		s.setFlash(w, r, "error", "Keine gültigen Zeilen (Gespann und Stunden erforderlich).")
	} else {
		s.audit(r, "quick_create", "entry", 0, fmt.Sprintf("%d Buchungen für %s", created, s.neighborName(r, neighborID)))
		s.setFlash(w, r, "success", fmt.Sprintf("%d Buchungen gespeichert.", created))
	}
	redirect(w, r, neighborURL(neighborID, yearID))
}

// buildGespannEntry resolves a fixed gespann into a snapshotted entry.
func (s *Server) buildGespannEntry(r *http.Request, gespannID int64, hours decimal.Decimal, dateStr string) (*models.Entry, []int64, bool) {
	g, err := s.store.GetGespann(r.Context(), gespannID)
	if err != nil || g.TractorID == nil || g.LoadLevelID == nil {
		return nil, nil, false
	}
	tractor, err := s.store.GetTractor(r.Context(), *g.TractorID)
	if err != nil {
		return nil, nil, false
	}
	load, err := s.store.GetLoadLevel(r.Context(), *g.LoadLevelID)
	if err != nil {
		return nil, nil, false
	}
	machines, err := s.store.MachinesByIDs(r.Context(), g.MachineIDs)
	if err != nil {
		return nil, nil, false
	}
	entryDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		entryDate = time.Now()
	}
	rate := calc.GespannRate(*tractor, *load, machines)
	names := make([]string, 0, len(machines))
	ids := make([]int64, 0, len(machines))
	for _, m := range machines {
		names = append(names, m.Name)
		ids = append(ids, m.ID)
	}
	gid := g.ID
	return &models.Entry{
		Date:          entryDate,
		TaskLabel:     g.Name,
		GespannID:     &gid,
		TractorID:     &tractor.ID,
		LoadLevelID:   &load.ID,
		TractorLabel:  tractor.Label(),
		LoadLabel:     load.Name,
		MachineLabels: strings.Join(names, ", "),
		Hours:         hours,
		HourlyRate:    rate,
		Cost:          calc.Cost(hours, rate),
	}, ids, true
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
	if year, err := s.store.GetBillingYear(r.Context(), entry.BillingYearID); err == nil && year.Completed() {
		s.setFlash(w, r, "error", "Das Abrechnungsjahr ist abgeschlossen – Buchungen können nicht mehr gelöscht werden.")
		redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
		return
	}
	if err := s.store.DeleteEntry(r.Context(), id); err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
	} else {
		s.audit(r, "delete", "entry", id, fmt.Sprintf("%s · %s, %s h, %s €",
			s.neighborName(r, entry.NeighborID), entry.TractorLabel,
			entry.Hours.StringFixed(2), entry.Cost.StringFixed(2)))
		s.setFlash(w, r, "success", "Buchung gelöscht.")
	}
	redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
}
