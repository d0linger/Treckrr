package server

import (
	"errors"
	"net/http"
	netmail "net/mail"
	"regexp"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
	"github.com/d0linger/treckrr/internal/web"
)

// neighborSummary is a neighbor with its totals for the selected billing year.
type neighborSummary struct {
	Neighbor      models.Neighbor
	Cost          decimal.Decimal
	Hours         decimal.Decimal
	Entries       int
	Paid          bool // fully settled (nothing remaining)
	Credit        bool // negative rest: the neighbor holds a Guthaben (I owe them)
	Remaining     decimal.Decimal
	QualityIssues int
}

// dashboardWorkItem is one actionable signal in the central work queue.
type dashboardWorkItem struct {
	Tone   string
	Icon   string
	Title  string
	Detail string
	Href   string
}

// handleDashboard renders the selected year's totals, setup state and central
// advisory work queue without changing any underlying workflow state.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	// One query for the whole per-neighbor breakdown (net, hours, count, paid),
	// replacing a 2+3N round-trip fan-out. Totals are summed in memory below.
	summaryRows, err := s.store.YearNeighborSummaries(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "dashboard: year neighbor summaries", err)
		return
	}

	summaries := make([]neighborSummary, 0, len(summaryRows))
	var grandCost, grandHours, paidCost, openCost, creditCost decimal.Decimal
	bookingCount, openCount, creditCount := 0, 0, 0
	for _, row := range summaryRows {
		summaries = append(summaries, neighborSummary{
			Neighbor: models.Neighbor{ID: row.NeighborID, Name: row.Name},
			Cost:     row.Cost, Hours: row.Hours, Entries: row.Entries,
			Paid: row.Paid, Credit: row.Credit, Remaining: row.Remaining,
		})
		grandCost = grandCost.Add(row.Cost)
		grandHours = grandHours.Add(row.Hours)
		bookingCount += row.Entries
		paidCost = paidCost.Add(row.PaidAmount) // actual money received
		if row.Remaining.IsPositive() {
			// "Offen" = what neighbors still owe (net minus payments). Negative
			// remainders (I owe them) are excluded from both count and sum so the
			// attention strip reports a consistent pair.
			openCost = openCost.Add(row.Remaining)
			openCount++
		}
		if row.Credit {
			// The reverse case: a Guthaben I still owe the neighbor (e.g. their
			// "verrechnete" work). Kept as its own pair, shown as a positive sum.
			creditCost = creditCost.Add(row.Remaining.Neg())
			creditCount++
		}
	}

	available, err := s.store.ListNeighborsNotInYear(r.Context(), year.ID)
	if err != nil {
		s.serverError(w, "dashboard: available neighbors", err)
		return
	}

	data := s.newPage(w, r, "Übersicht", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, "dashboard: year selector", err)
		return
	}
	// Offer "carry over neighbors from the previous year" when one exists and
	// there are members not yet in this year.
	if prev, err := s.store.PreviousBillingYear(r.Context(), year.Year); err == nil {
		current := map[int64]bool{}
		for _, sm := range summaries {
			current[sm.Neighbor.ID] = true
		}
		prevMembers, err := s.store.ListYearNeighbors(r.Context(), prev.ID)
		if err != nil {
			s.serverError(w, "dashboard: previous-year members", err)
			return
		}
		var candidates []models.Neighbor
		for _, n := range prevMembers {
			if !current[n.ID] && !n.Archived {
				candidates = append(candidates, n)
			}
		}
		if len(candidates) > 0 {
			data["PrevYear"] = prev.Year
			data["PrevNeighbors"] = candidates
		}
	}
	data["Summaries"] = summaries
	data["Available"] = available
	data["GrandCost"] = grandCost
	data["GrandHours"] = grandHours
	data["NeighborCount"] = len(summaries)
	data["BookingCount"] = bookingCount
	data["Completed"] = year.Completed()
	data["PaidCost"] = paidCost
	data["OpenCost"] = openCost
	data["OpenCount"] = openCount
	data["CreditCost"] = creditCost
	data["CreditCount"] = creditCount
	quality, err := s.loadDataQuality(r, year)
	if err != nil {
		s.serverError(w, "dashboard: data quality", err)
		return
	}
	for i := range summaries {
		summaries[i].QualityIssues = quality.NeighborCounts[summaries[i].Neighbor.ID]
	}
	// Refresh after attaching per-neighbor completeness counts.
	data["Summaries"] = summaries
	// How many bookings are out of sync with the current basis (open years only).
	// Gate first (0040): one indexed count answers "could anything be stale?".
	// When it says no — the normal case, since the basis is rarely edited — the
	// full repricing simulation is skipped entirely instead of running on every
	// dashboard render. Only when it fires does the exact preview decide the
	// number shown, so the displayed count keeps its meaning.
	staleCount := 0
	if !year.Completed() {
		if maybe, err := s.store.CountPotentiallyStale(r.Context(), year.ID, nil); err == nil && maybe > 0 {
			if rows, err := s.store.RecalcPreview(r.Context(), year.ID, nil); err == nil {
				for _, ro := range rows {
					if ro.Changed {
						staleCount++
					}
				}
			}
		}
	}
	data["StaleCount"] = staleCount

	work := make([]dashboardWorkItem, 0, 8)
	if len(quality.Issues) > 0 {
		tone := "warn"
		if quality.High > 0 {
			tone = "bad"
		}
		work = append(work, dashboardWorkItem{
			Tone: tone, Icon: "!", Title: strconv.Itoa(len(quality.Issues)) + " Stammdaten-Hinweis(e)",
			Detail: strconv.Itoa(quality.High) + " wichtig · " + strconv.Itoa(quality.Medium) + " ergänzen",
			Href:   "/data-quality?year=" + strconv.FormatInt(year.ID, 10),
		})
	}
	if !year.Completed() && staleCount > 0 {
		work = append(work, dashboardWorkItem{Tone: "warn", Icon: "↻",
			Title:  strconv.Itoa(staleCount) + " Buchung(en) neu berechnen",
			Detail: "Grundlage wurde geändert", Href: "/years/" + strconv.FormatInt(year.ID, 10) + "/recalc"})
	}
	if year.Completed() && openCount > 0 {
		work = append(work, dashboardWorkItem{Tone: "owe", Icon: "€",
			Title: strconv.Itoa(openCount) + " Nachbar(n)", Detail: "mit offener Zahlung · " + web.Money(openCost), Href: "#offene-zahlungen"})
	}
	if year.Completed() && creditCount > 0 {
		work = append(work, dashboardWorkItem{Tone: "credit", Icon: "€",
			Title: strconv.Itoa(creditCount) + " Nachbar(n)", Detail: "mit Guthaben – noch auszuzahlen · " + web.Money(creditCost), Href: "#auszuzahlen"})
	}
	if user := userFromCtx(r); user != nil && user.CanWrite() {
		if ops, err := s.store.OperationsStatus(r.Context()); err == nil {
			if ops.Failed+ops.Ambiguous+ops.Held > 0 && user.IsAdmin {
				work = append(work, dashboardWorkItem{Tone: "bad", Icon: "✉",
					Title:  strconv.Itoa(ops.Failed+ops.Ambiguous+ops.Held) + " E-Mail-Entscheidung(en)",
					Detail: "fehlgeschlagen, unklar oder angehalten", Href: "/admin/mail"})
			}
			if ops.RecurringBlocked > 0 {
				work = append(work, dashboardWorkItem{Tone: "warn", Icon: "↻",
					Title:  strconv.Itoa(ops.RecurringBlocked) + " blockierte Serie(n)",
					Detail: "erfordern eine fachliche Korrektur", Href: "/recurring"})
			}
		}
		if user.IsAdmin {
			backup := s.backupHealth()
			if backup.Tone != "ok" {
				work = append(work, dashboardWorkItem{Tone: backup.Tone, Icon: "B", Title: backup.Title,
					Detail: backup.AgeLabel, Href: "/admin/backup"})
			}
		}
	}
	data["WorkItems"] = work

	// First-run onboarding: nudge the operator through setup until all steps are
	// done, then it disappears on its own (no dismiss needed). "Basis" is implicitly
	// complete once the dashboard renders (a year requires a base).
	company, _ := s.store.GetCompany(r.Context())
	hasNeighbor := len(summaries) > 0
	if !hasNeighbor {
		hasNeighbor, _ = s.store.AnyNeighbors(r.Context())
	}
	hasBooking := false
	for _, sm := range summaries {
		if sm.Entries > 0 {
			hasBooking = true
			break
		}
	}
	if !hasBooking { // a fresh year has no summaries; check globally like hasNeighbor
		hasBooking, _ = s.store.AnyEntries(r.Context())
	}
	companyOK := strings.TrimSpace(company.Name) != ""
	if !companyOK || !hasNeighbor || !hasBooking {
		data["Onboarding"] = []map[string]any{
			{"Done": companyOK, "Text": "Betriebsdaten hinterlegen", "Href": "/admin/company"},
			{"Done": hasNeighbor, "Text": "Ersten Nachbarn anlegen", "Href": "/neighbors"},
			{"Done": hasBooking, "Text": "Erste Buchung erfassen", "Href": dashboardURL(year.ID)},
		}
	}
	s.render(w, r, "dashboard", data)
}

// handleYearAddNeighbor adds an existing neighbor to the billing year.
func (s *Server) handleYearAddNeighbor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	if yearID == 0 || neighborID == 0 {
		s.setFlash(w, r, "error", "Bitte einen Nachbarn wählen.")
		redirect(w, r, dashboardURL(yearID))
		return
	}
	if err := s.store.AddNeighborToYear(r.Context(), yearID, neighborID); err != nil {
		s.serverError(w, "add neighbor to year", err)
		return
	}
	s.audit(r, "add_neighbor", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
	s.setFlash(w, r, "success", "Nachbar zum Jahr hinzugefügt.")
	redirect(w, r, dashboardURL(yearID))
}

// handleYearRemoveNeighbor removes a neighbor from the year (membership only).
// It refuses while the neighbor still has ANY record in that year — bookings,
// ledger postings, payments, documents, installments or dunning/send history.
// Removal would orphan them: still counted in the year total but invisible in
// the per-neighbor/payment views and impossible to reverse, because every
// account writer requires the membership. The check and the removal are one
// locked store transaction (see store.RemoveNeighborFromYear), so a booking
// written concurrently cannot slip in between.
func (s *Server) handleYearRemoveNeighbor(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	neighborID := formInt64(r, "neighbor_id")
	err := s.store.RemoveNeighborFromYear(r.Context(), yearID, neighborID)
	var inUse *store.MembershipInUseError
	switch {
	case errors.As(err, &inUse):
		s.setFlash(w, r, "error", "Nachbar hat noch "+membershipDependentLabel(inUse.Kind)+
			" in diesem Jahr und kann nicht entfernt werden.")
		redirect(w, r, dashboardURL(yearID))
		return
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "info", "Nachbar ist in diesem Jahr nicht (mehr) vorhanden.")
		redirect(w, r, dashboardURL(yearID))
		return
	case err != nil:
		s.serverError(w, "remove neighbor from year", err)
		return
	}
	s.audit(r, "remove_neighbor", "year", yearID, s.neighborName(r, neighborID)+" · Jahr "+s.yearLabel(r, yearID))
	s.setFlash(w, r, "success", "Nachbar aus dem Jahr entfernt.")
	redirect(w, r, dashboardURL(yearID))
}

// membershipDependentLabel names a store.MembershipInUseError kind in German.
func membershipDependentLabel(kind string) string {
	switch kind {
	case "entries":
		return "Buchungen"
	case "ledger":
		return "Verrechnungspositionen (Konto)"
	case "payments":
		return "Zahlungen"
	case "invoices":
		return "Belege (Rechnung, Abschlag oder Gutschrift)"
	case "payment_plans":
		return "Raten im Ratenplan"
	case "beleg_sends":
		return "vermerkte Beleg-Versendungen"
	case "dunning_notices":
		return "Mahnungen"
	}
	return "Einträge"
}

// handleNeighborUpdate changes a neighbor's name, note, address, and tax id.
func (s *Server) handleNeighborUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	name := trimmed(r, "name")
	note := trimmed(r, "note")
	address := trimmed(r, "address")
	taxID := trimmed(r, "tax_id")
	email := trimmed(r, "email")
	if name == "" {
		s.setFlash(w, r, "error", "Name darf nicht leer sein.")
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "Name", name, maxNameLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "Notiz", note, maxNoteLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "Adresse", address, maxNoteLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "UID/Steuernummer", taxID, maxNameLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if s.tooLong(w, r, "E-Mail", email, maxNameLen) {
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	if email != "" {
		if _, err := netmail.ParseAddress(email); err != nil {
			s.setFlash(w, r, "error", "Ungültige E-Mail-Adresse.")
			redirect(w, r, neighborReturnURL(r, id))
			return
		}
	}
	// The IBAN is a matcher key for the bank import, so it is stored normalized
	// (no spaces, upper case) and shape-checked — a typo would otherwise just
	// silently never match a credit.
	iban := strings.ToUpper(strings.ReplaceAll(trimmed(r, "iban"), " ", ""))
	if iban != "" && !ibanShape.MatchString(iban) {
		s.setFlash(w, r, "error", "Ungültige IBAN.")
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	before, _ := s.store.GetNeighbor(r.Context(), id)
	einvoice := models.InvoiceParty{
		Street: trimmed(r, "einvoice_street"), ZIP: trimmed(r, "einvoice_zip"),
		Town: trimmed(r, "einvoice_town"), CountryCode: strings.ToUpper(trimmed(r, "einvoice_country_code")),
		OrderID: trimmed(r, "einvoice_order_id"),
	}
	if einvoice.CountryCode == "" {
		einvoice.CountryCode = "AT"
	}
	if !countryCodeShape.MatchString(einvoice.CountryCode) {
		s.setFlash(w, r, "error", "E-Rechnungs-Ländercode muss aus zwei Buchstaben bestehen (z. B. AT).")
		redirect(w, r, neighborReturnURL(r, id))
		return
	}
	for label, value := range map[string]string{
		"E-Rechnungs-Straße": einvoice.Street, "E-Rechnungs-PLZ": einvoice.ZIP,
		"E-Rechnungs-Ort": einvoice.Town, "Auftragsreferenz": einvoice.OrderID,
	} {
		if s.tooLong(w, r, label, value, maxNameLen) {
			redirect(w, r, neighborReturnURL(r, id))
			return
		}
	}
	// Leeres Feld = Firmenstandard (NULL), sonst 0-365 Tage.
	var paymentTerm *int
	if v := strings.TrimSpace(r.FormValue("payment_term_days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 365 {
			s.setFlash(w, r, "error", "Zahlungsziel muss eine ganze Zahl zwischen 0 und 365 Tagen sein.")
			redirect(w, r, neighborReturnURL(r, id))
			return
		}
		paymentTerm = &n
	}
	// A refused update (anonymized or unknown neighbor) writes nothing, so it is
	// neither reported as success nor audited: the append-only log must not
	// record a diff that never reached the database.
	if err := s.store.UpdateNeighbor(r.Context(), id, name, note, address, taxID, email, iban, paymentTerm, einvoice); errors.Is(err, store.ErrNeighborAnonymized) {
		s.setFlash(w, r, "error", "Dieser Nachbar wurde anonymisiert — seine Daten können nicht mehr bearbeitet werden.")
	} else if errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Nachbar nicht gefunden.")
	} else if err != nil {
		s.setFlash(w, r, "error", "Aktualisierung fehlgeschlagen.")
	} else {
		detail := name
		if before != nil {
			d := diffFields(
				fieldChange{"Name", before.Name, name},
				fieldChange{"Notiz", before.Note, note},
				fieldChange{"Adresse", before.Address, address},
				fieldChange{"UID/Steuernr.", before.TaxID, taxID},
			)
			// Masked like the company IBAN: the audit log keeps only the change
			// marker, never the full account number.
			if m := ibanChangeMarker(before.IBAN, iban); m != "" {
				if d == "" {
					d = m
				} else {
					d += " · " + m
				}
			}
			if d != "" {
				detail = d
			}
		}
		s.audit(r, "update", "neighbor", id, detail)
		s.setFlash(w, r, "success", "Nachbar aktualisiert.")
	}
	redirect(w, r, neighborReturnURL(r, id))
}

// ibanShape is the light structural IBAN check (country, check digits, BBAN).
var ibanShape = regexp.MustCompile(`^[A-Z]{2}[0-9]{2}[A-Z0-9]{11,30}$`)

// neighborReturnURL points back to the central neighbor page when the request
// originated there, otherwise to the neighbor within the current year.
func neighborReturnURL(r *http.Request, id int64) string {
	if r.FormValue("origin") == "manage" {
		return "/neighbors"
	}
	return neighborURL(id, formInt64(r, "year_id"))
}

// handleNeighborDelete deletes the neighbor globally (all years and entries).
func (s *Server) handleNeighborDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	// A neighbor carrying ANY financial or tax-relevant history must not be
	// deleted — the row is referenced ON DELETE CASCADE by entries, payments,
	// neighbor_ledger and invoices alike, so a delete that only checked bookings
	// silently destroyed carry-forwards, payments and festgeschriebene Rechnungen
	// (§ 132 BAO keeps those for seven years). Deactivating is the way out.
	blockers, err := s.store.NeighborDeleteBlockers(r.Context(), id)
	if err != nil {
		s.serverError(w, "neighbor delete: count references", err)
		return
	}
	if blockers.Any() {
		s.setFlash(w, r, "error", "Nachbar hat "+describeDeleteBlockers(blockers)+
			" und kann nicht gelöscht werden. Bitte stattdessen deaktivieren.")
	} else {
		before, _ := s.store.GetNeighbor(r.Context(), id)
		if err := s.store.DeleteNeighbor(r.Context(), id); errors.Is(err, store.ErrHasHistory) {
			// The database refused: a record landed between the check above and the
			// delete. Same message as the precheck, so the race is invisible.
			s.setFlash(w, r, "error", "Nachbar hat inzwischen Buchungen oder Zahlungen "+
				"und kann nicht gelöscht werden. Bitte stattdessen deaktivieren.")
		} else if err != nil {
			s.setFlash(w, r, "error", "Löschen fehlgeschlagen.")
		} else {
			detail := ""
			if before != nil {
				detail = before.Name
			}
			s.audit(r, "delete", "neighbor", id, detail)
			s.setFlash(w, r, "success", "Nachbar gelöscht.")
		}
	}
	if r.FormValue("origin") == "manage" {
		redirect(w, r, "/neighbors")
		return
	}
	redirect(w, r, dashboardURL(s.yearIDFromForm(r)))
}

// handleNeighborAnonymize erases a neighbor's live personal data (DSGVO Art. 17)
// while keeping their bookings and the frozen invoice snapshots, which are under
// a legal retention obligation. Irreversible; audited.
func (s *Server) handleNeighborAnonymize(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	// Ausbaukarte 88: typed confirmation, like the restore. This deletes free
	// text and photos for good; a mis-aimed click must not be enough.
	if strings.TrimSpace(r.FormValue("confirm")) != "ANONYMISIEREN" {
		s.setFlash(w, r, "error", "Zum Anonymisieren bitte ANONYMISIEREN eintippen (Großschreibung beachten).")
		redirect(w, r, "/neighbors")
		return
	}
	before, _ := s.store.GetNeighbor(r.Context(), id)
	if err := s.store.AnonymizeNeighbor(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w, r)
			return
		}
		s.setFlash(w, r, "error", "Anonymisieren fehlgeschlagen.")
	} else {
		detail := ""
		if before != nil {
			detail = before.Name + " → anonymisiert"
		}
		s.audit(r, "anonymize", "neighbor", id, detail)
		s.setFlash(w, r, "success", "Nachbar anonymisiert. Rechnungen bleiben aufbewahrungspflichtig erhalten.")
	}
	redirect(w, r, "/neighbors")
}
