package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

func neighborEquipmentURL(neighborID int64) string {
	return fmt.Sprintf("/neighbors/%d/equipment", neighborID)
}

// handleNeighborEquipment renders the equipment supplied by one neighbor.
func (s *Server) handleNeighborEquipment(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), neighborID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	equipment, err := s.store.ListNeighborEquipment(r.Context(), neighborID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	data := s.newPage(w, r, "Fremdgeräte · "+neighbor.Name, "neighbors")
	data["Neighbor"], data["Equipment"] = neighbor, equipment
	s.render(w, r, "neighbor_equipment", data)
}

// neighborEquipmentForm validates reusable foreign-equipment master data.
func (s *Server) neighborEquipmentForm(w http.ResponseWriter, r *http.Request, neighborID int64) (models.NeighborEquipment, bool) {
	equipment := models.NeighborEquipment{
		NeighborID: neighborID,
		Name:       trimmed(r, "name"), CapacityUnit: trimmed(r, "capacity_unit"),
		BillingUnit: trimmed(r, "billing_unit"), Note: trimmed(r, "note"),
	}
	if equipment.Name == "" || equipment.BillingUnit == "" {
		s.setFlash(w, r, "error", "Bezeichnung und Abrechnungseinheit sind erforderlich.")
		return equipment, false
	}
	for _, field := range []struct {
		label, value string
		max          int
	}{
		{"Bezeichnung", equipment.Name, maxNameLen},
		{"Fassungs-Einheit", equipment.CapacityUnit, 16},
		{"Abrechnungseinheit", equipment.BillingUnit, 16},
		{"Notiz", equipment.Note, maxNoteLen},
	} {
		if s.tooLong(w, r, field.label, field.value, field.max) {
			return equipment, false
		}
	}
	var ok bool
	if raw := trimmed(r, "capacity"); raw != "" {
		equipment.Capacity, ok = boundedEquipmentDecimal(raw, true)
		if !ok || !equipment.Capacity.IsPositive() || equipment.CapacityUnit == "" {
			s.setFlash(w, r, "error", "Fassungsvermögen bitte positiv und gemeinsam mit einer Einheit angeben.")
			return equipment, false
		}
	} else if equipment.CapacityUnit != "" {
		s.setFlash(w, r, "error", "Zur Fassungs-Einheit fehlt das Fassungsvermögen.")
		return equipment, false
	}
	if raw := trimmed(r, "default_rate"); raw != "" {
		equipment.DefaultRate, ok = boundedEquipmentDecimal(raw, false)
		if !ok {
			s.setFlash(w, r, "error", "Der Vorschlagssatz muss eine nicht negative Zahl mit höchstens vier Nachkommastellen sein.")
			return equipment, false
		}
	}
	return equipment, true
}

func boundedEquipmentDecimal(raw string, positive bool) (decimal.Decimal, bool) {
	value, ok := parseGermanDecimalOK(raw)
	if !ok || value.IsNegative() || value.Exponent() < -4 || value.GreaterThanOrEqual(decimal.NewFromInt(1_000_000_000)) {
		return decimal.Zero, false
	}
	if positive && !value.IsPositive() {
		return decimal.Zero, false
	}
	return value, true
}

// handleNeighborEquipmentCreate adds reusable equipment for a neighbor.
func (s *Server) handleNeighborEquipmentCreate(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	equipment, ok := s.neighborEquipmentForm(w, r, neighborID)
	if !ok {
		redirect(w, r, neighborEquipmentURL(neighborID))
		return
	}
	id, err := s.store.CreateNeighborEquipment(r.Context(), equipment)
	if err != nil {
		s.setFlash(w, r, "error", "Fremdgerät konnte nicht angelegt werden (Bezeichnung bereits vorhanden?).")
	} else {
		s.audit(r, "create", "neighbor_equipment", id, equipment.Name)
		s.setFlash(w, r, "success", "Fremdgerät angelegt.")
	}
	redirect(w, r, neighborEquipmentURL(neighborID))
}

// handleNeighborEquipmentUpdate changes reusable master data only.
func (s *Server) handleNeighborEquipmentUpdate(w http.ResponseWriter, r *http.Request) {
	neighborID, equipmentID, ok := neighborEquipmentPath(w, r, s)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	equipment, valid := s.neighborEquipmentForm(w, r, neighborID)
	if !valid {
		redirect(w, r, neighborEquipmentURL(neighborID))
		return
	}
	equipment.ID = equipmentID
	if err := s.store.UpdateNeighborEquipment(r.Context(), equipment); err != nil {
		s.setFlash(w, r, "error", "Fremdgerät konnte nicht gespeichert werden (Bezeichnung bereits vorhanden?).")
	} else {
		s.audit(r, "update", "neighbor_equipment", equipmentID, equipment.Name)
		s.setFlash(w, r, "success", "Fremdgerät aktualisiert.")
	}
	redirect(w, r, neighborEquipmentURL(neighborID))
}

// handleNeighborEquipmentArchive hides or restores equipment for new bookings.
func (s *Server) handleNeighborEquipmentArchive(w http.ResponseWriter, r *http.Request) {
	neighborID, equipmentID, ok := neighborEquipmentPath(w, r, s)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden.")
		return
	}
	archived := trimmed(r, "archived") == "true"
	if err := s.store.SetNeighborEquipmentArchived(r.Context(), equipmentID, neighborID, archived); err != nil {
		s.setFlash(w, r, "error", "Status konnte nicht geändert werden.")
	} else {
		action, message := "reactivate", "Fremdgerät reaktiviert."
		if archived {
			action, message = "archive", "Fremdgerät archiviert."
		}
		s.audit(r, action, "neighbor_equipment", equipmentID, fmt.Sprintf("Nachbar #%d", neighborID))
		s.setFlash(w, r, "success", message)
	}
	redirect(w, r, neighborEquipmentURL(neighborID))
}

// handleNeighborEquipmentDelete removes only equipment without booking history.
func (s *Server) handleNeighborEquipmentDelete(w http.ResponseWriter, r *http.Request) {
	neighborID, equipmentID, ok := neighborEquipmentPath(w, r, s)
	if !ok {
		return
	}
	switch err := s.store.DeleteNeighborEquipment(r.Context(), equipmentID, neighborID); {
	case errors.Is(err, store.ErrHasHistory):
		s.setFlash(w, r, "error", "Dieses Fremdgerät wurde bereits gebucht – bitte archivieren statt löschen.")
	case errors.Is(err, store.ErrNotFound):
		s.notFound(w, r)
		return
	case err != nil:
		s.setFlash(w, r, "error", "Fremdgerät konnte nicht gelöscht werden.")
	default:
		s.audit(r, "delete", "neighbor_equipment", equipmentID, fmt.Sprintf("Nachbar #%d", neighborID))
		s.setFlash(w, r, "success", "Fremdgerät gelöscht.")
	}
	redirect(w, r, neighborEquipmentURL(neighborID))
}

func neighborEquipmentPath(w http.ResponseWriter, r *http.Request, s *Server) (int64, int64, bool) {
	neighborID, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return 0, 0, false
	}
	equipmentID, err := formInt64FromPath(r, "equipmentID")
	if err != nil || equipmentID <= 0 {
		s.notFound(w, r)
		return 0, 0, false
	}
	return neighborID, equipmentID, true
}

func equipmentSnapshotLabel(e *models.NeighborEquipment) string {
	if e == nil {
		return ""
	}
	if !e.Capacity.IsPositive() {
		return e.Name
	}
	return strings.TrimSpace(e.Name + " · " + e.Capacity.String() + " " + e.CapacityUnit)
}
