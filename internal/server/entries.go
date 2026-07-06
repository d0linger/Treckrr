package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

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
		Cost      float64
		Hours     float64
		Paid      bool
		Completed bool
	}
	var rows []yearRow
	var totalCost, totalHours float64
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
		totalCost += cost
		totalHours += hours
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
	Hours float64
	Cost  float64
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
		s.Hours += e.Hours
		s.Cost += e.Cost
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
	for _, t := range tractors {
		out.Tractors = append(out.Tractors, apiTractor{ID: t.ID, PS: t.PS})
	}
	for _, l := range loads {
		out.Loads = append(out.Loads, apiLoad{ID: l.ID, Cost: l.CostPerPS})
	}
	for _, m := range machines {
		out.Machines = append(out.Machines, apiMachine{ID: m.ID, Rate: calc.MachineRate(m)})
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
	s.audit(r, "create", "entry", newID, fmt.Sprintf("%s · %s, %.2f h × %.2f = %.2f €",
		s.neighborName(r, neighborID), entry.TaskLabel, entry.Hours, entry.HourlyRate, entry.Cost))
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
	hours := formFloat(r, "hours")
	if hours <= 0 {
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
	s.audit(r, "update", "entry", id, fmt.Sprintf("%.2f h × %.2f = %.2f €", entry.Hours, entry.HourlyRate, entry.Cost))
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
		s.audit(r, "void", "entry", id, fmt.Sprintf("%s · %.2f € %s", nb, entry.Cost, reason))
		s.setFlash(w, r, "success", "Buchung storniert.")
	} else {
		s.audit(r, "unvoid", "entry", id, nb)
		s.setFlash(w, r, "success", "Stornierung aufgehoben.")
	}
	redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
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
		hours := 0.0
		if i < len(hoursList) {
			hours = parseGermanFloat(hoursList[i])
		}
		if gid == 0 || hours <= 0 {
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
func (s *Server) buildGespannEntry(r *http.Request, gespannID int64, hours float64, dateStr string) (*models.Entry, []int64, bool) {
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
		s.audit(r, "delete", "entry", id, fmt.Sprintf("%s · %s, %.2f h, %.2f €",
			s.neighborName(r, entry.NeighborID), entry.TractorLabel, entry.Hours, entry.Cost))
		s.setFlash(w, r, "success", "Buchung gelöscht.")
	}
	redirect(w, r, neighborURL(entry.NeighborID, entry.BillingYearID))
}
