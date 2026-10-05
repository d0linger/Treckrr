package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/models"
)

// tractorView pairs a tractor with its computed rates per load level.
type tractorRateView struct {
	Tractor models.Tractor
	Rates   []loadRate
}

type loadRate struct {
	Load models.LoadLevel
	Rate decimal.Decimal
}

func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request) {
	base, ok := s.resolveBase(w, r)
	if !ok {
		return
	}
	loads, _ := s.store.ListLoadLevels(r.Context(), base.ID)
	tractors, _ := s.store.ListTractors(r.Context(), base.ID)
	machines, _ := s.store.ListMachines(r.Context(), base.ID)
	adjustments, err := s.store.ListFuelAdjustments(r.Context(), base.ID)
	if err != nil {
		s.serverError(w, "prices: fuel adjustments", err)
		return
	}

	var tractorViews []tractorRateView
	for _, t := range tractors {
		var rates []loadRate
		for _, l := range loads {
			rates = append(rates, loadRate{Load: l, Rate: calc.TractorRate(t, l)})
		}
		tractorViews = append(tractorViews, tractorRateView{Tractor: t, Rates: rates})
	}

	type machineView struct {
		Machine     models.Machine
		Rate        decimal.Decimal
		Proposal    decimal.Decimal
		HasProposal bool
	}
	var machineViews []machineView
	for _, m := range machines {
		proposal, hasProposal := m.CalculatedSelfCost()
		machineViews = append(machineViews, machineView{
			Machine: m, Rate: calc.MachineRate(m), Proposal: proposal, HasProposal: hasProposal,
		})
	}

	cats, _ := s.store.MachineCategories(r.Context(), base.ID)

	data := s.newPage(w, r, "Kosten verwalten", "prices")
	data["Base"] = base
	data["Categories"] = cats
	data["Loads"] = loads
	data["TractorViews"] = tractorViews
	data["MachineViews"] = machineViews
	data["FuelAdjustments"] = adjustments
	data["Locked"] = base.Locked
	data["EditMode"] = r.URL.Query().Get("mode") == "edit"
	data["PriceSection"] = priceSection(r.URL.Query().Get("section"))
	s.render(w, r, "prices", data)
}

// priceSection keeps the rate editor on one supported catalog area. Unknown
// links fall back to machines, the most frequently maintained price list.
func priceSection(raw string) string {
	switch raw {
	case "fuel", "loads", "tractors", "machines":
		return raw
	default:
		return "machines"
	}
}

// handleFuelAdjustmentSave stores one effective-dated, non-cumulative hourly
// addition. Historical bookings remain untouched until explicit recalculation.
func (s *Server) handleFuelAdjustmentSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	baseID := s.baseIDFromForm(r)
	back := pricesURL(baseID) + "#fuel-adjustments"
	if s.lockedRedirect(w, r, baseID, back) {
		return
	}
	label := trimmed(r, "label")
	if label == "" || lenError("Bezeichnung", label, maxNameLen) != "" {
		s.setFlash(w, r, "error", "Bitte eine Bezeichnung mit höchstens 100 Zeichen angeben.")
		redirect(w, r, back)
		return
	}
	effective, err := time.Parse("2006-01-02", trimmed(r, "effective_from"))
	if err != nil {
		s.setFlash(w, r, "error", "Bitte einen gültigen Stichtag angeben.")
		redirect(w, r, back)
		return
	}
	rawAmount := strings.TrimSpace(r.FormValue("amount_per_h"))
	amount, ok := parseGermanDecimalOK(rawAmount)
	if !ok || amount.IsNegative() || amount.GreaterThan(decimal.NewFromInt(1_000_000)) {
		s.setFlash(w, r, "error", "Der Zuschlag muss zwischen 0 und 1.000.000 €/h liegen.")
		redirect(w, r, back)
		return
	}
	adjustment := &models.FuelAdjustment{
		ID: formInt64(r, "id"), BaseID: baseID, EffectiveFrom: effective,
		Label: label, AmountPerH: amount,
	}
	if err := s.store.SaveFuelAdjustment(r.Context(), adjustment); err != nil {
		s.setFlash(w, r, "error", "Anpassung konnte nicht gespeichert werden. Pro Stichtag ist nur eine Version möglich.")
		redirect(w, r, back)
		return
	}
	s.audit(r, "save", "fuel_adjustment", adjustment.ID,
		label+" ab "+effective.Format("02.01.2006")+": "+amount.String()+" €/h")
	s.setFlash(w, r, "success", "Anpassung gespeichert. Bestehende Buchungen bleiben bis zur ausdrücklichen Neuberechnung unverändert.")
	redirect(w, r, back)
}

// handleFuelAdjustmentDelete removes a version without rewriting snapshots.
func (s *Server) handleFuelAdjustmentDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	back := pricesURL(baseID) + "#fuel-adjustments"
	if s.lockedRedirect(w, r, baseID, back) {
		return
	}
	if err := s.store.DeleteFuelAdjustment(r.Context(), baseID, id); err != nil {
		s.flashDeleted(w, r, err)
		redirect(w, r, back)
		return
	}
	s.audit(r, "delete", "fuel_adjustment", id, "")
	s.setFlash(w, r, "success", "Anpassung gelöscht. Bereits gebuchte Sätze bleiben unverändert.")
	redirect(w, r, back)
}

// handleMachineCostModel stores transparent calculation assumptions. The
// current rate changes only when the operator explicitly selects "apply".
func (s *Server) handleMachineCostModel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	baseID := s.baseIDFromForm(r)
	back := pricesURL(baseID) + "#machine-" + itoa64(id)
	if s.lockedRedirect(w, r, baseID, back) {
		return
	}
	fields := []struct{ name, label string }{
		{"acquisition_cost", "Anschaffungskosten"}, {"residual_value", "Restwert"},
		{"annual_hours", "Jahresstunden"}, {"fuel_cost_per_h", "Treibstoff je Stunde"},
		{"annual_maintenance", "Wartung"}, {"annual_insurance", "Versicherung"},
		{"annual_other_cost", "sonstige Jahreskosten"},
	}
	values := make(map[string]decimal.Decimal, len(fields))
	for _, field := range fields {
		raw := strings.TrimSpace(r.FormValue(field.name))
		if s.tooLong(w, r, field.label, raw, maxDecimalLen) {
			redirect(w, r, back)
			return
		}
		if raw == "" {
			values[field.name] = decimal.Zero
			continue
		}
		value, ok := parseGermanDecimalOK(raw)
		if !ok || value.IsNegative() {
			s.setFlash(w, r, "error", field.label+" muss eine nichtnegative Zahl sein.")
			redirect(w, r, back)
			return
		}
		values[field.name] = value
	}
	years := formInt(r, "useful_years")
	if years < 0 || years > 100 {
		s.setFlash(w, r, "error", "Nutzungsdauer muss zwischen 0 und 100 Jahren liegen.")
		redirect(w, r, back)
		return
	}
	if values["residual_value"].GreaterThan(values["acquisition_cost"]) {
		s.setFlash(w, r, "error", "Der Restwert darf die Anschaffungskosten nicht übersteigen.")
		redirect(w, r, back)
		return
	}
	machine := models.Machine{
		ID: id, AcquisitionCost: values["acquisition_cost"], ResidualValue: values["residual_value"],
		UsefulYears: years, AnnualHours: values["annual_hours"], FuelCostPerH: values["fuel_cost_per_h"],
		AnnualMaintenance: values["annual_maintenance"], AnnualInsurance: values["annual_insurance"],
		AnnualOtherCost: values["annual_other_cost"],
	}
	apply := r.FormValue("action") == "apply"
	if apply {
		if _, ok := machine.CalculatedSelfCost(); !ok {
			s.setFlash(w, r, "error", "Für die Übernahme sind Nutzungsdauer und Jahresstunden größer 0 erforderlich.")
			redirect(w, r, back)
			return
		}
	}
	if err := s.store.UpdateMachineCostModel(r.Context(), baseID, machine, apply); err != nil {
		s.setFlash(w, r, "error", "Kostenmodell konnte nicht gespeichert werden.")
	} else if apply {
		proposal, _ := machine.CalculatedSelfCost()
		s.audit(r, "apply_cost_model", "machine", id, "Selbstkosten "+proposal.StringFixed(2)+" €/h")
		s.setFlash(w, r, "success", "Kostenmodell gespeichert und Vorschlag ausdrücklich übernommen.")
	} else {
		s.audit(r, "update_cost_model", "machine", id, "Annahmen gespeichert; aktiver Satz unverändert")
		s.setFlash(w, r, "success", "Annahmen gespeichert. Der aktive Selbstkostensatz bleibt unverändert.")
	}
	redirect(w, r, back)
}

// lockedRedirect reports whether the base is locked; if so it flashes and
// redirects to the given URL and returns true.
func (s *Server) lockedRedirect(w http.ResponseWriter, r *http.Request, baseID int64, target string) bool {
	base, err := s.store.GetBase(r.Context(), baseID)
	if err != nil {
		s.badRequest(w, "Unbekannte Bemessungsgrundlage")
		return true
	}
	if base.Locked {
		s.setFlash(w, r, "error", "Diese Bemessungsgrundlage ist gesperrt und kann nicht geändert werden.")
		redirect(w, r, target)
		return true
	}
	return false
}

// ---- Load levels ---------------------------------------------------------

func (s *Server) handleLoadLevelSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.tooLong(
		w, r, "Kosten je PS", r.FormValue("cost_per_ps"), maxDecimalLen,
	) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	name := trimmed(r, "name")
	cost := formDecimal(r, "cost_per_ps")
	sort := formInt(r, "sort_order")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	var err error
	action := "update"
	if id == 0 {
		action = "create"
		id, err = s.store.CreateLoadLevel(r.Context(), baseID, name, cost, sort)
	} else {
		err = s.store.UpdateLoadLevel(r.Context(), id, name, cost, sort)
	}
	if err == nil {
		s.audit(r, action, "load_level", id, name)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// handleLoadLevelDelete checks the selected basis lock before deleting a load
// level, then reports the store result and audits a successful deletion.
func (s *Server) handleLoadLevelDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	before, _ := s.store.GetLoadLevel(r.Context(), id)
	err = s.store.DeleteLoadLevel(r.Context(), id)
	if err == nil {
		detail := ""
		if before != nil {
			detail = before.Name
		}
		s.audit(r, "delete", "load_level", id, detail)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// ---- Tractors ------------------------------------------------------------

func (s *Server) handleTractorSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.tooLong(
		w, r, "PS", r.FormValue("ps"), maxDecimalLen,
	) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	ident := trimmed(r, "ident")
	name := trimmed(r, "name")
	ps := formDecimal(r, "ps")
	sortOrder := formInt(r, "sort_order")
	if ident == "" || !ps.IsPositive() {
		s.setFlash(w, r, "error", "Bezeichnung und PS (> 0) sind erforderlich.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.tooLong(w, r, "Bezeichnung", ident, maxNameLen) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	var err error
	action := "update"
	if id == 0 {
		action = "create"
		id, err = s.store.CreateTractor(r.Context(), baseID, ident, name, ps, sortOrder)
	} else {
		err = s.store.UpdateTractor(r.Context(), id, ident, name, ps, sortOrder)
	}
	if err == nil {
		s.audit(r, action, "tractor", id, ident)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// handleTractorToggle changes availability for new bookings without removing the
// tractor from existing bookings. Changes are blocked by the selected basis lock.
func (s *Server) handleTractorToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	active := r.FormValue("active") == "true"
	label := s.tractorLabel(r, &id)
	if err := s.store.SetTractorActive(r.Context(), id, active); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if active {
		s.audit(r, "activate", "tractor", id, label)
		s.setFlash(w, r, "success", "Traktor aktiviert.")
	} else {
		s.audit(r, "deactivate", "tractor", id, label)
		s.setFlash(w, r, "success", "Traktor deaktiviert (bleibt für bestehende Buchungen erhalten).")
	}
	redirect(w, r, pricesURL(baseID))
}

// handleTractorDelete checks the selected basis lock before deleting a tractor,
// then reports the store result and audits a successful deletion.
func (s *Server) handleTractorDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	before, _ := s.store.GetTractor(r.Context(), id)
	err = s.store.DeleteTractor(r.Context(), id)
	if err == nil {
		detail := ""
		if before != nil {
			detail = before.Label()
		}
		s.audit(r, "delete", "tractor", id, detail)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// ---- Machines ------------------------------------------------------------

func (s *Server) handleMachineSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	baseID := s.baseIDFromForm(r)
	for _, field := range []struct{ name, label string }{
		{name: "working_width", label: "Arbeitsbreite"},
		{name: "cost_per_ab", label: "Kosten"},
		{name: "self_cost_per_h", label: "Selbstkosten"},
	} {
		if s.tooLong(
			w, r, field.label, r.FormValue(field.name), maxDecimalLen,
		) {
			redirect(w, r, pricesURL(baseID))
			return
		}
	}
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	id := formInt64(r, "id")
	name := trimmed(r, "name")
	width := formDecimal(r, "working_width")
	cost := formDecimal(r, "cost_per_ab")
	category := trimmed(r, "category")
	sortOrder := formInt(r, "sort_order")
	if name == "" || !width.IsPositive() {
		s.setFlash(w, r, "error", "Name und Arbeitsbreite (> 0) sind erforderlich.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	if s.tooLong(w, r, "Kategorie", category, maxNameLen) {
		redirect(w, r, pricesURL(baseID))
		return
	}
	// Selbstkosten je Einsatzstunde (Ausbaukarte 83): optional, never negative,
	// 0 = not configured — the Deckungsbeitrag is then simply not shown.
	selfCost := formDecimal(r, "self_cost_per_h")
	if selfCost.IsNegative() {
		s.setFlash(w, r, "error", "Die Selbstkosten dürfen nicht negativ sein.")
		redirect(w, r, pricesURL(baseID))
		return
	}
	var err error
	action := "update"
	if id == 0 {
		action = "create"
		id, err = s.store.CreateMachine(r.Context(), baseID, name, width, cost, category, sortOrder, selfCost)
	} else {
		err = s.store.UpdateMachine(r.Context(), id, name, width, cost, category, sortOrder, selfCost)
	}
	if err == nil {
		s.audit(r, action, "machine", id, name)
	}
	s.flashSaved(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// handleMachineToggle changes availability for new bookings while preserving
// existing machine references. Changes are blocked by the selected basis lock.
func (s *Server) handleMachineToggle(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	active := r.FormValue("active") == "true"
	name := s.machineNames(r, []int64{id})
	if err := s.store.SetMachineActive(r.Context(), id, active); err != nil {
		s.setFlash(w, r, "error", "Aktion fehlgeschlagen.")
	} else if active {
		s.audit(r, "activate", "machine", id, name)
		s.setFlash(w, r, "success", "Maschine aktiviert.")
	} else {
		s.audit(r, "deactivate", "machine", id, name)
		s.setFlash(w, r, "success", "Maschine deaktiviert (bleibt für bestehende Buchungen erhalten).")
	}
	redirect(w, r, pricesURL(baseID))
}

// handleMachineDelete checks the selected basis lock before deleting a machine,
// then reports the store result and audits a successful deletion.
func (s *Server) handleMachineDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	baseID := s.baseIDFromForm(r)
	if s.lockedRedirect(w, r, baseID, pricesURL(baseID)) {
		return
	}
	name := ""
	if ms, mErr := s.store.MachinesByIDs(r.Context(), []int64{id}); mErr == nil && len(ms) > 0 {
		name = ms[0].Name
	}
	err = s.store.DeleteMachine(r.Context(), id)
	if err == nil {
		s.audit(r, "delete", "machine", id, name)
	}
	s.flashDeleted(w, r, err)
	redirect(w, r, pricesURL(baseID))
}

// ---- flash helpers -------------------------------------------------------

func (s *Server) flashSaved(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.setFlash(w, r, "error", "Speichern fehlgeschlagen (evtl. Name bereits vergeben).")
		return
	}
	s.setFlash(w, r, "success", "Gespeichert.")
}

func (s *Server) flashDeleted(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		s.setFlash(w, r, "error", "Löschen fehlgeschlagen (evtl. noch in Verwendung).")
		return
	}
	s.setFlash(w, r, "success", "Gelöscht.")
}
