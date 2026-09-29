package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/store"
)

// rolloverURL is the resumable assistant for one source year.
func rolloverURL(sourceID int64) string {
	return "/years/" + strconv.FormatInt(sourceID, 10) + "/wechsel"
}

// handleYearRollover renders a live preview of every existing or pending step.
// It never performs writes on GET and keeps balance transfers as explicit
// per-account actions in the established dashboard workflow.
func (s *Server) handleYearRollover(w http.ResponseWriter, r *http.Request) {
	sourceID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	source, err := s.store.GetBillingYear(r.Context(), sourceID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	target, err := s.store.BillingYearByNumber(r.Context(), source.Year+1)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "year rollover: target", err)
		return
	}
	checks, err := s.store.YearClosingChecks(r.Context(), source.ID)
	if err != nil {
		s.serverError(w, "year rollover: closing checks", err)
		return
	}
	summaries, err := s.store.YearNeighborSummaries(r.Context(), source.ID)
	if err != nil {
		s.serverError(w, "year rollover: balances", err)
		return
	}
	var openAmount, creditAmount decimal.Decimal
	openCount, creditCount := 0, 0
	for _, row := range summaries {
		if row.Remaining.IsPositive() {
			openCount++
			openAmount = openAmount.Add(row.Remaining)
		} else if row.Remaining.IsNegative() {
			creditCount++
			creditAmount = creditAmount.Add(row.Remaining.Neg())
		}
	}
	sourceMembers, err := s.store.ListYearNeighbors(r.Context(), source.ID)
	if err != nil {
		s.serverError(w, "year rollover: source members", err)
		return
	}
	targetMembers := 0
	pendingMembers := 0
	if target != nil {
		members, err := s.store.ListYearNeighbors(r.Context(), target.ID)
		if err != nil {
			s.serverError(w, "year rollover: target members", err)
			return
		}
		targetMembers = len(members)
		present := make(map[int64]bool, len(members))
		for _, member := range members {
			present[member.ID] = true
		}
		for _, member := range sourceMembers {
			if !member.Archived && !present[member.ID] {
				pendingMembers++
			}
		}
	} else {
		for _, member := range sourceMembers {
			if !member.Archived {
				pendingMembers++
			}
		}
	}
	rules, err := s.store.ListRecurring(r.Context())
	if err != nil {
		s.serverError(w, "year rollover: recurring", err)
		return
	}
	activeRules, blockedRules := 0, 0
	for _, rule := range rules {
		if rule.Active {
			activeRules++
		}
		if rule.LastError != "" {
			blockedRules++
		}
	}
	bases, err := s.store.ListBases(r.Context())
	if err != nil {
		s.serverError(w, "year rollover: bases", err)
		return
	}
	data := s.newPage(w, r, "Geführter Jahreswechsel", "years")
	data["Source"] = source
	data["Target"] = target
	data["TargetYear"] = source.Year + 1
	data["Bases"] = bases
	data["Checks"] = checks
	data["OpenChecks"] = store.OpenClosingChecks(checks)
	data["OpenCount"], data["OpenAmount"] = openCount, openAmount
	data["CreditCount"], data["CreditAmount"] = creditCount, creditAmount
	data["SourceMembers"], data["TargetMembers"] = len(sourceMembers), targetMembers
	data["PendingMembers"] = pendingMembers
	data["ActiveRules"], data["BlockedRules"] = activeRules, blockedRules
	data["Backup"] = s.backupHealth()
	s.render(w, r, "year_rollover", data)
}

// handleYearRolloverCreate creates only the next calendar year. Repeating the
// action returns the existing target; optional basis cloning is cleaned up if a
// concurrent request wins the unique-year race.
func (s *Server) handleYearRolloverCreate(w http.ResponseWriter, r *http.Request) {
	sourceID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	source, err := s.store.GetBillingYear(r.Context(), sourceID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := rolloverURL(sourceID)
	if !source.Completed() {
		s.setFlash(w, r, "error", "Bitte zuerst das Ausgangsjahr abschließen.")
		redirect(w, r, back)
		return
	}
	if target, err := s.store.BillingYearByNumber(r.Context(), source.Year+1); err == nil && target != nil {
		s.setFlash(w, r, "info", "Das Folgejahr ist bereits angelegt; der Schritt wurde übersprungen.")
		redirect(w, r, back)
		return
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "year rollover: check target", err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	targetYear := source.Year + 1
	baseID := formInt64(r, "base_id")
	clonedBaseID := int64(0)
	if r.FormValue("base_mode") == "clone" {
		clonedBaseID, err = s.store.CloneBase(r.Context(), source.BaseID, targetYear, "Bemessungsgrundlage "+strconv.Itoa(targetYear))
		if err != nil {
			s.serverError(w, "year rollover: clone base", err)
			return
		}
		baseID = clonedBaseID
	} else {
		bases, listErr := s.store.ListBases(r.Context())
		if listErr != nil {
			s.serverError(w, "year rollover: bases", listErr)
			return
		}
		valid := false
		for _, base := range bases {
			valid = valid || base.ID == baseID
		}
		if baseID == 0 || !valid {
			s.setFlash(w, r, "error", "Bitte eine gültige Preisgrundlage auswählen.")
			redirect(w, r, back)
			return
		}
	}
	targetID, err := s.store.CreateBillingYear(r.Context(), targetYear, baseID, "Abrechnung "+strconv.Itoa(targetYear))
	if err != nil {
		if clonedBaseID != 0 {
			_ = s.store.DeleteBase(r.Context(), clonedBaseID)
		}
		if existing, getErr := s.store.BillingYearByNumber(r.Context(), targetYear); getErr == nil && existing != nil {
			s.setFlash(w, r, "info", "Das Folgejahr wurde parallel bereits angelegt; der Schritt wurde übersprungen.")
			redirect(w, r, back)
			return
		}
		s.serverError(w, "year rollover: create target", err)
		return
	}
	s.audit(r, "rollover_create", "year", targetID, fmt.Sprintf("Folgejahr %d aus %d", targetYear, source.Year))
	s.setFlash(w, r, "success", "Folgejahr angelegt. Jetzt können die Nachbarn übernommen werden.")
	redirect(w, r, back)
}

// handleYearRolloverNeighbors copies active source membership into the open
// target year with ON CONFLICT semantics, so retries never duplicate members.
func (s *Server) handleYearRolloverNeighbors(w http.ResponseWriter, r *http.Request) {
	sourceID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := rolloverURL(sourceID)
	source, err := s.store.GetBillingYear(r.Context(), sourceID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	target, err := s.store.BillingYearByNumber(r.Context(), source.Year+1)
	if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Bitte zuerst das Folgejahr anlegen.")
		redirect(w, r, back)
		return
	}
	if err != nil {
		s.serverError(w, "year rollover: target", err)
		return
	}
	if !source.Completed() || target.Completed() {
		s.setFlash(w, r, "error", "Übernahme ist nur aus einem abgeschlossenen in ein offenes Folgejahr möglich.")
		redirect(w, r, back)
		return
	}
	added, err := s.store.CarryYearNeighbors(r.Context(), source.ID, target.ID)
	if err != nil {
		s.serverError(w, "year rollover: carry neighbors", err)
		return
	}
	if added == 0 {
		s.setFlash(w, r, "info", "Alle aktiven Nachbarn sind bereits enthalten; nichts doppelt angelegt.")
	} else {
		s.audit(r, "rollover_neighbors", "year", target.ID, plural(added, "Nachbar", "Nachbarn")+" aus "+strconv.Itoa(source.Year))
		s.setFlash(w, r, "success", plural(added, "Nachbar", "Nachbarn")+" übernommen.")
	}
	redirect(w, r, back)
}
