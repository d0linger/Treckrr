package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// maxPlotNumberLen bounds a Schlagnummer.
const maxPlotNumberLen = 40

// bookingFieldFromForm resolves the optional field of an own booking: one of
// the neighbor's listed fields (field_id) or a free-text one (field_id=other
// with field_label). A new booking may not pick an archived field.
func (s *Server) bookingFieldFromForm(r *http.Request, neighborID int64, creating bool) (*int64, string, string, error) {
	raw, text := trimmed(r, "field_id"), trimmed(r, "field_label")
	switch raw {
	case "":
		return nil, "", "", nil
	case "other":
		if lenError("Feldname", text, maxNameLen) != "" {
			return nil, "", "Bitte den Feldnamen mit höchstens 100 Zeichen angeben.", nil
		}
		return nil, text, "", nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, "", "Bitte ein gültiges Feld wählen.", nil
	}
	field, err := s.store.GetField(r.Context(), neighborID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", "Das gewählte Feld gehört nicht zu diesem Nachbarn.", nil
	}
	if err != nil {
		return nil, "", "", err
	}
	if field.Archived && creating {
		return nil, "", "Das Feld „" + field.Name + "“ ist archiviert. Bitte ein aktives Feld wählen.", nil
	}
	return &field.ID, field.Name, "", nil
}

// fieldsData adds a neighbor's fields and their work in the year to a page.
func (s *Server) fieldsData(ctx context.Context, data pageData, neighborID, yearID int64) error {
	fields, err := s.store.ListFields(ctx, neighborID)
	if err != nil {
		return err
	}
	stats, err := s.store.FieldStatsForYear(ctx, yearID, &neighborID, time.Time{}, time.Time{})
	if err != nil {
		return err
	}
	byField := map[int64]store.FieldStat{}
	var loose []store.FieldStat
	for _, st := range stats {
		if st.FieldID != nil {
			byField[*st.FieldID] = st
		} else {
			loose = append(loose, st)
		}
	}
	data["Fields"] = fields
	data["FieldStats"] = byField
	data["LooseFieldStats"] = loose
	return nil
}

// handleFieldCreate adds a field to the neighbor's list.
func (s *Server) handleFieldCreate(w http.ResponseWriter, r *http.Request) {
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
	back := neighborURL(id, formInt64(r, "year")) + "#felder"
	if neighbor.Anonymized {
		s.setFlash(w, r, "error", "Für einen anonymisierten Nachbarn können keine Felder angelegt werden.")
		redirect(w, r, back)
		return
	}
	f := models.Field{NeighborID: id, Name: strings.TrimSpace(r.FormValue("name")), PlotNumber: strings.TrimSpace(r.FormValue("plot_number"))}
	if f.Name == "" || lenError("Feldname", f.Name, maxNameLen) != "" || len([]rune(f.PlotNumber)) > maxPlotNumberLen {
		s.setFlash(w, r, "error", "Bitte einen Feldnamen (höchstens 100 Zeichen) und eine Schlagnummer mit höchstens 40 Zeichen angeben.")
		redirect(w, r, back)
		return
	}
	if raw := strings.TrimSpace(r.FormValue("area_ha")); raw != "" {
		area, ok := parseGermanDecimalOK(raw)
		if !ok || !area.IsPositive() || area.GreaterThanOrEqual(decimal.NewFromInt(1_000_000)) || !area.Equal(area.Round(4)) {
			s.setFlash(w, r, "error", "Bitte die Fläche in Hektar über 0 mit höchstens vier Nachkommastellen angeben.")
			redirect(w, r, back)
			return
		}
		f.AreaHa = area
	}
	if _, err := s.store.CreateField(r.Context(), f); errors.Is(err, store.ErrFieldExists) {
		s.setFlash(w, r, "error", "Ein aktives Feld „"+f.Name+"“ gibt es bei diesem Nachbarn schon.")
		redirect(w, r, back)
		return
	} else if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "field_create", "neighbor", id, neighbor.Name+" · Feld "+f.Label())
	s.setFlash(w, r, "success", "Feld „"+f.Name+"“ angelegt. Es steht ab sofort beim Buchen zur Wahl.")
	redirect(w, r, back)
}

// handleFieldArchive hides a field from new bookings or brings it back.
func (s *Server) handleFieldArchive(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := neighborURL(id, formInt64(r, "year")) + "#felder"
	fieldID, archive := formInt64(r, "field_id"), r.FormValue("archived") == "1"
	field, err := s.store.GetField(r.Context(), id, fieldID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	switch err := s.store.SetFieldArchived(r.Context(), id, fieldID, archive); {
	case errors.Is(err, store.ErrFieldExists):
		s.setFlash(w, r, "error", "Es gibt schon ein aktives Feld „"+field.Name+"“.")
	case err != nil:
		s.serverError(w, r.URL.Path, err)
		return
	case archive:
		s.audit(r, "field_archive", "neighbor", id, s.neighborName(r, id)+" · Feld "+field.Name+" archiviert")
		s.setFlash(w, r, "success", "Feld „"+field.Name+"“ archiviert. Gebuchte Leistungen behalten den Feldnamen.")
	default:
		s.audit(r, "field_restore", "neighbor", id, s.neighborName(r, id)+" · Feld "+field.Name+" wieder aktiv")
		s.setFlash(w, r, "success", "Feld „"+field.Name+"“ ist wieder aktiv.")
	}
	redirect(w, r, back)
}

// handleFieldDelete removes a field no booking names.
func (s *Server) handleFieldDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	back := neighborURL(id, formInt64(r, "year")) + "#felder"
	fieldID := formInt64(r, "field_id")
	field, err := s.store.GetField(r.Context(), id, fieldID)
	if err != nil {
		s.notFound(w, r)
		return
	}
	deleted, err := s.store.DeleteField(r.Context(), id, fieldID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !deleted {
		s.setFlash(w, r, "info", "Das Feld „"+field.Name+"“ ist gebucht und kann nur archiviert werden.")
		redirect(w, r, back)
		return
	}
	s.audit(r, "field_delete", "neighbor", id, s.neighborName(r, id)+" · Feld "+field.Name+" gelöscht")
	s.setFlash(w, r, "success", "Feld „"+field.Name+"“ gelöscht.")
	redirect(w, r, back)
}

// meterReadingsAttr serializes the last hour-meter end reading per tractor of
// a basis for the booking form, which prefills the next start from it.
func (s *Server) meterReadingsAttr(ctx context.Context, baseID int64) (string, error) {
	last, err := s.store.LastMeterReadings(ctx, baseID)
	if err != nil || len(last) == 0 {
		return "", err
	}
	out := make(map[string]string, len(last))
	for id, reading := range last {
		out[strconv.FormatInt(id, 10)] = reading.String()
	}
	b, err := json.Marshal(out)
	return string(b), err
}
