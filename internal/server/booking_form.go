package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

const maxBookingPeople = 20

// bookingPeopleFromForm validates aligned component rows. Empty rows are optional;
// disabled controls must never shift one person's hours onto another person's ID.
func (s *Server) bookingPeopleFromForm(r *http.Request, kind, direction string, previous []models.BookingPerson) ([]models.BookingPerson, string, error) {
	fields := []string{"person_row_id", "person_id", "person_name", "person_hours", "person_rate", "person_state"}
	n := len(r.Form[fields[0]])
	if n > maxBookingPeople+3 {
		return nil, "Pro Buchung sind höchstens 20 Personen möglich.", nil
	}
	for _, name := range fields {
		if len(r.Form[name]) != n {
			return nil, "Die Personenzeilen sind unvollständig. Bitte die Buchung neu laden.", nil
		}
	}
	if trimmed(r, "copy_mode") == "1" && trimmed(r, "copy_people") != "1" && kind != "labor" {
		return nil, "", nil
	}
	old := make(map[int64]models.BookingPerson, len(previous))
	for _, person := range previous {
		old[person.ID] = person
	}
	seen := map[int64]bool{}
	people := make([]models.BookingPerson, 0, n)
	for i := range n {
		value := func(name string) string { return strings.TrimSpace(r.Form[name][i]) }
		state := value("person_state")
		if state != "active" && state != "void" && state != "remove" {
			return nil, "Bitte einen gültigen Personenstatus wählen.", nil
		}
		rowID, err := strconv.ParseInt(orDefault(value("person_row_id"), "0"), 10, 64)
		if err != nil || rowID < 0 || (rowID > 0 && seen[rowID]) {
			return nil, "Ungültige oder doppelte Personenzeile.", nil
		}
		prior, existed := old[rowID]
		if rowID > 0 && !existed {
			return nil, "Die Personenzeile gehört nicht zu dieser Buchung.", nil
		}
		seen[rowID] = true
		if state == "remove" {
			if existed {
				prior.Voided = true
				people = append(people, prior)
			}
			continue
		}
		pidRaw, name := value("person_id"), value("person_name")
		if pidRaw == "" && name == "" && value("person_hours") == "" && value("person_rate") == "" && rowID == 0 {
			continue
		}
		p := models.BookingPerson{ID: rowID, Name: name, Voided: state == "void"}
		defaultRate := decimal.Zero
		if pidRaw != "" {
			pid, err := strconv.ParseInt(pidRaw, 10, 64)
			if err != nil || pid <= 0 {
				return nil, "Bitte eine gültige Person wählen.", nil
			}
			person, err := s.store.GetPerson(r.Context(), pid)
			if errors.Is(err, store.ErrNotFound) {
				return nil, "Die gewählte Person ist nicht mehr vorhanden. Bitte neu wählen oder den Namen frei eingeben.", nil
			}
			if err != nil {
				return nil, "", err
			}
			p.PersonID, p.Name, defaultRate = &person.ID, person.Name, person.HourlyRate
			if prior.PersonID != nil && *prior.PersonID == pid {
				p.Name, defaultRate = prior.Name, prior.Rate
			}
		}
		if p.Name == "" || lenError("Person", p.Name, maxNameLen) != "" {
			return nil, "Bitte eine Person wählen oder einen Namen mit höchstens 100 Zeichen eingeben.", nil
		}
		hours := value("person_hours")
		if hours == "" && (kind == "equipment" || kind == "labor") {
			hours = trimmed(r, "hours")
		}
		p.Hours, err = bookingDecimal(hours)
		if err != nil {
			return nil, "Bitte für jede Person positive Mannstunden angeben (höchstens vier Nachkommastellen).", nil
		}
		rate := value("person_rate")
		if rate == "" && direction == "out" {
			rate = defaultRate.String()
		}
		p.Rate, err = bookingDecimal(rate)
		if err != nil || !p.Hours.Mul(p.Rate).Round(2).IsPositive() {
			return nil, "Bitte für jede Person einen positiven vereinbarten Stundensatz angeben.", nil
		}
		people = append(people, p)
	}
	if len(people) > maxBookingPeople {
		return nil, "Pro Buchung sind höchstens 20 Personen möglich.", nil
	}
	if kind == "labor" && (len(people) == 0 || people[0].Voided) {
		return nil, "Für Mannstunden ist mindestens eine aktive Person erforderlich.", nil
	}
	return people, "", nil
}

// bookingDecimal applies the same bounded decimal rules to each repeated row.
func bookingDecimal(raw string) (decimal.Decimal, error) {
	r := &http.Request{Form: url.Values{"value": {raw}}}
	v, valid := positiveBookingDecimal(r, "value")
	if !valid {
		return decimal.Zero, errors.New("invalid booking decimal")
	}
	return v, nil
}

// bookingEntryPerson snapshots a person's line without accepting a client total.
func bookingEntryPerson(p models.BookingPerson, main *models.Entry) *models.Entry {
	return &models.Entry{
		ID: p.ID, NeighborID: main.NeighborID, BillingYearID: main.BillingYearID,
		Date: main.Date, TaskLabel: "Mannstunden " + p.Name, Unit: models.UnitMannstunde,
		Quantity: p.Hours, UnitPrice: p.Rate, Cost: p.Hours.Mul(p.Rate).Round(2),
		PersonID: p.PersonID, PersonName: p.Name, Voided: p.Voided,
		RequestFingerprint: main.RequestFingerprint,
	}
}

// entryBookingPerson converts a stored labor line into the common editor model.
func entryBookingPerson(e models.Entry) models.BookingPerson {
	name := e.PersonName
	if name == "" {
		name = strings.TrimPrefix(e.TaskLabel, "Mannstunden ")
	}
	if strings.TrimSpace(name) == "" {
		name = "Nicht zugeordnet"
	}
	return models.BookingPerson{ID: e.ID, PersonID: e.PersonID, Name: name,
		Hours: e.Quantity, Rate: e.UnitPrice, Voided: e.Voided}
}

// parseBookingV2 resolves shared controls for all types without conflating own
// invoice-bearing entries with independently priced account counterclaims.
func (s *Server) parseBookingV2(r *http.Request, previous []models.BookingPerson, previousEquipmentID *int64) (*models.Entry, []int64, *store.LedgerBookingInput, []models.BookingPerson, string, error) {
	kind, direction, msg := unifiedBookingSelection(r)
	if msg != "" {
		return nil, nil, nil, nil, msg, nil
	}
	date, err := time.Parse("2006-01-02", trimmed(r, "entry_date"))
	if err != nil {
		return nil, nil, nil, nil, "Bitte ein gültiges Datum angeben.", nil
	}
	task, note := trimmed(r, "task_label"), trimmed(r, "note")
	if task == "" || lenError("Tätigkeit", task, maxNameLen) != "" || lenError("Notiz", note, maxNoteLen) != "" {
		return nil, nil, nil, nil, "Bitte eine Beschreibung mit höchstens 100 Zeichen und eine Notiz mit höchstens 2000 Zeichen eingeben.", nil
	}
	people, msg, err := s.bookingPeopleFromForm(r, kind, direction, previous)
	if msg != "" || err != nil {
		return nil, nil, nil, nil, msg, err
	}
	year, err := s.store.GetBillingYear(r.Context(), formInt64(r, "year_id"))
	if err != nil {
		return nil, nil, nil, nil, "", err
	}
	main := &models.Entry{Date: date, TaskLabel: task, Note: note, NeighborID: formInt64(r, "neighbor_id"),
		BillingYearID: year.ID, IdempotencyKey: trimmed(r, "idempotency_key"), RequestFingerprint: unifiedRequestFingerprint(r)}
	if lenError("Buchungskennung", main.IdempotencyKey, maxNameLen) != "" {
		return nil, nil, nil, nil, "Die Buchungskennung ist zu lang.", nil
	}
	b := models.LedgerBooking{Version: 1, Kind: kind, TaskLabel: task, Note: note, People: people}
	var foreignEquipment *models.NeighborEquipment
	if equipmentID := formInt64(r, "neighbor_equipment_id"); equipmentID != 0 {
		if direction != "in" || (kind != "equipment" && kind != "quantity") {
			return nil, nil, nil, nil, "Fremdgeräte können nur für Gegenleistungen des Nachbarn gewählt werden.", nil
		}
		foreignEquipment, err = s.store.GetNeighborEquipment(r.Context(), equipmentID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, nil, nil, "Das gewählte Fremdgerät ist nicht mehr vorhanden.", nil
		}
		if err != nil {
			return nil, nil, nil, nil, "", err
		}
		if foreignEquipment.NeighborID != main.NeighborID {
			return nil, nil, nil, nil, "Das gewählte Fremdgerät gehört nicht zu diesem Nachbarn.", nil
		}
		retainingArchived := previousEquipmentID != nil && *previousEquipmentID == foreignEquipment.ID
		if foreignEquipment.Archived && !retainingArchived {
			return nil, nil, nil, nil, "Das gewählte Fremdgerät ist archiviert. Bitte reaktivieren oder ein anderes wählen.", nil
		}
		b.NeighborEquipmentID = &foreignEquipment.ID
		b.PartnerLabel = equipmentSnapshotLabel(foreignEquipment)
		b.EquipmentCapacity = foreignEquipment.Capacity
		b.EquipmentCapacityUnit = foreignEquipment.CapacityUnit
		b.EquipmentBillingUnit = foreignEquipment.BillingUnit
	}
	var machines []int64
	switch kind {
	case "labor":
		p := people[0]
		main.Unit, main.Quantity, main.UnitPrice = models.UnitMannstunde, p.Hours, p.Rate
		main.PersonID, main.PersonName, main.Cost = p.PersonID, p.Name, p.Cost()
		b.Unit, b.Quantity, b.UnitPrice, b.PartnerPerson, b.PersonID = main.Unit, p.Hours, p.Rate, p.Name, p.PersonID
	case "quantity", "fixed":
		ledger, message := ledgerBookingFromForm(r, kind, direction)
		if message != "" {
			return nil, nil, nil, nil, message, nil
		}
		b.Unit, b.Quantity, b.UnitPrice = ledger.Booking.Unit, ledger.Booking.Quantity, ledger.Booking.UnitPrice
		if foreignEquipment != nil {
			if equipmentIsHourly(foreignEquipment.BillingUnit) {
				return nil, nil, nil, nil, "Stundenbasierte Fremdgeräte bitte als Traktor / Gespann / Gefährt erfassen.", nil
			}
			b.Unit = foreignEquipment.BillingUnit
		}
		main.Unit, main.Quantity, main.UnitPrice = b.Unit, b.Quantity, b.UnitPrice
		main.Cost = b.Quantity.Mul(b.UnitPrice).Round(2)
	case "equipment":
		hours, valid := positiveBookingDecimal(r, "hours")
		if !valid || (direction == "out" && !store.MachineHoursRepresentable(hours)) {
			return nil, nil, nil, nil, "Bitte gültige Stunden angeben. Eigene Maschinenstunden erlauben höchstens drei Nachkommastellen.", nil
		}
		mode := trimmed(r, "mode")
		if foreignEquipment != nil {
			mode = "free"
			if !equipmentIsHourly(foreignEquipment.BillingUnit) {
				return nil, nil, nil, nil, "Dieses Fremdgerät wird nicht nach Stunden abgerechnet. Bitte Mengenleistung wählen.", nil
			}
		}
		if mode != "gespann" && mode != "manual" && mode != "free" {
			return nil, nil, nil, nil, "Bitte eine gültige Zusammenstellung wählen.", nil
		}
		if mode != "free" {
			resolved, ids, message := s.resolveEntryFromForm(r)
			if message != "" {
				return nil, nil, nil, nil, message, nil
			}
			if message, err := s.checkBookingCatalog(r, resolved, ids, year.Base.ID); message != "" || err != nil {
				return nil, nil, nil, nil, message, err
			}
			resolved.NeighborID, resolved.BillingYearID = main.NeighborID, main.BillingYearID
			resolved.IdempotencyKey, resolved.RequestFingerprint = main.IdempotencyKey, main.RequestFingerprint
			main, machines = resolved, ids
			b.GespannID, b.TractorID, b.LoadLevelID, b.MachineIDs = main.GespannID, main.TractorID, main.LoadLevelID, ids
			b.PartnerLabel = strings.Trim(strings.Join([]string{main.TractorLabel, main.LoadLabel, main.MachineLabels}, " · "), " ·")
			if main.GespannID != nil {
				g, err := s.store.GetGespann(r.Context(), *main.GespannID)
				if err != nil {
					return nil, nil, nil, nil, "", err
				}
				b.PartnerLabel = g.Name
			}
		} else {
			if foreignEquipment == nil {
				b.PartnerLabel = trimmed(r, "partner_label")
			}
			if b.PartnerLabel == "" || lenError("Fahrzeug", b.PartnerLabel, maxNameLen) != "" {
				return nil, nil, nil, nil, "Bitte das Fahrzeug oder Gespann mit höchstens 100 Zeichen beschreiben.", nil
			}
			main.MachineLabels = b.PartnerLabel
		}
		rate := main.HourlyRate
		if direction == "in" || mode == "free" {
			var valid bool
			rate, valid = positiveBookingDecimal(r, "partner_rate")
			if !valid {
				return nil, nil, nil, nil, "Bitte den vereinbarten Maschinensatz angeben.", nil
			}
		}
		main.Unit, main.Hours, main.Quantity, main.HourlyRate, main.UnitPrice = "h", hours, hours, rate, rate
		main.Cost = hours.Mul(rate).Round(2)
		b.Mode, b.Unit, b.Quantity, b.UnitPrice = mode, "h", hours, rate
	}
	if !b.Total().IsPositive() || b.Total().GreaterThanOrEqual(decimal.NewFromInt(10_000_000_000)) {
		return nil, nil, nil, nil, "Der Gesamtbetrag liegt außerhalb des zulässigen Bereichs.", nil
	}
	if direction == "in" || kind == "fixed" {
		// Component IDs remain stable across edits; new rows get a monotonic local ID.
		maxID := int64(0)
		for _, p := range previous {
			maxID = max(maxID, p.ID)
		}
		for i := range b.People {
			if b.People[i].ID == 0 {
				maxID++
				b.People[i].ID = maxID
			}
		}
		in := &store.LedgerBookingInput{YearID: year.ID, NeighborID: main.NeighborID, Date: date,
			Incoming: direction == "in", Booking: b, IdempotencyKey: main.IdempotencyKey}
		return nil, nil, in, people, "", nil
	}
	return main, machines, nil, people, "", nil
}

// checkBookingCatalog prevents cross-price-basis references in counterclaims,
// whose JSON references do not have the foreign-key checks of outgoing entries.
func (s *Server) checkBookingCatalog(r *http.Request, entry *models.Entry, ids []int64, baseID int64) (string, error) {
	invalid := "Die Auswahl gehört nicht zur Preisgrundlage dieses Jahres. Bitte neu wählen."
	if entry.GespannID != nil {
		g, err := s.store.GetGespann(r.Context(), *entry.GespannID)
		if err != nil {
			return "", err
		}
		if g.BaseID != baseID {
			return invalid, nil
		}
	}
	if entry.TractorID != nil {
		t, err := s.store.GetTractor(r.Context(), *entry.TractorID)
		if err != nil {
			return "", err
		}
		if t.BaseID != baseID {
			return invalid, nil
		}
	}
	if entry.LoadLevelID != nil {
		l, err := s.store.GetLoadLevel(r.Context(), *entry.LoadLevelID)
		if err != nil {
			return "", err
		}
		if l.BaseID != baseID {
			return invalid, nil
		}
	}
	machines, err := s.store.MachinesByIDs(r.Context(), ids)
	if err != nil {
		return "", err
	}
	for _, machine := range machines {
		if machine.BaseID != baseID {
			return invalid, nil
		}
	}
	return "", nil
}

// handleBookingCreateV2 stores the shared editor's complete group atomically.
func (s *Server) handleBookingCreateV2(w http.ResponseWriter, r *http.Request) {
	entry, ids, ledger, people, msg, err := s.parseBookingV2(r, nil, nil)
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	if msg != "" {
		s.rejectUnifiedBooking(w, r, msg)
		return
	}
	var mainID int64
	if ledger != nil {
		mainID, err = s.store.CreateLedgerBooking(r.Context(), *ledger)
	} else {
		helpers := bookingHelpers(entry, people)
		var helperIDs []int64
		mainID, helperIDs, err = s.store.CreateEntryGroup(r.Context(), entry, ids, helpers)
		if mainID == 0 {
			for _, id := range helperIDs {
				mainID = max(mainID, id)
			}
		}
	}
	if err != nil {
		s.unifiedBookingError(w, r, err)
		return
	}
	if r.Header.Get("X-Offline-Replay") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	msg = "Buchung gespeichert."
	if mainID == 0 {
		msg = "Buchung war bereits erfasst."
	}
	s.setFlash(w, r, "success", msg)
	redirect(w, r, neighborURL(formInt64(r, "neighbor_id"), formInt64(r, "year_id")))
}

// bookingHelpers separates a labor booking's primary person from its companions.
func bookingHelpers(entry *models.Entry, people []models.BookingPerson) []*models.Entry {
	if entry.Unit == models.UnitMannstunde && len(people) > 0 {
		people = people[1:]
	}
	helpers := make([]*models.Entry, 0, len(people))
	for _, p := range people {
		helpers = append(helpers, bookingEntryPerson(p, entry))
	}
	return helpers
}

func equipmentIsHourly(unit string) bool {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "h", "std", "std.", "stunde", "stunden":
		return true
	default:
		return false
	}
}
