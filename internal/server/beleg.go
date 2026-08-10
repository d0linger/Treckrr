package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"treckrr/internal/calc"
	"treckrr/internal/models"
	"treckrr/internal/store"
)

// BelegTractorLoad is one Belastungsstufe a tractor was used at this year: its
// €/PS·h, the resulting tractor €/h, and the machines run with that combination.
type BelegTractorLoad struct {
	Load     string
	CostPS   string
	Rate     decimal.Decimal
	Machines []string
}

// BelegTractor groups the load levels a tractor was used at this year. Ident and
// PS fill the left "rail" column of the Traktoren table.
type BelegTractor struct {
	Ident string
	PS    string
	Loads []BelegTractorLoad
}

// BelegMachine is one machine used this year, with its €/AB·h and €/h.
type BelegMachine struct {
	Name   string
	Width  string
	CostAB string
	Rate   decimal.Decimal
}

// BelegDay groups a neighbor's bookings by calendar day so the date is shown once
// per day (a left rail marks the continuation rows), keeping a long list compact.
type BelegDay struct {
	Date    string
	Entries []models.Entry
}

// BelegService is one distinct Leistung aggregated across the year — booking
// count, total hours and total cost — for the optional "Bündeln" (grouped) view.
type BelegService struct {
	Label string
	Count int
	Hours decimal.Decimal
	Cost  decimal.Decimal
}

// deu formats a decimal in German notation (comma) without trailing zeros,
// e.g. 1.1500 -> "1,15", 0.4752 -> "0,4752", 100 -> "100".
func deu(d decimal.Decimal) string {
	s := d.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.ReplaceAll(s, ".", ",")
}

// deu2 is like deu but keeps at least two decimals, for unit prices (€/PS·h,
// €/AB·h): 0.4 -> "0,40", 12 -> "12,00", 0.4752 -> "0,4752".
func deu2(d decimal.Decimal) string {
	s := deu(d)
	if i := strings.IndexByte(s, ','); i < 0 {
		s += ",00"
	} else if n := len(s) - i - 1; n < 2 {
		s += strings.Repeat("0", 2-n)
	}
	return s
}

// handleNeighborBeleg renders a compact, share-friendly statement for one
// neighbor and year (bookings + ledger + saldo) — a clean list to screenshot
// and hand over. Read-only; no actions, no editing.
func (s *Server) handleNeighborBeleg(w http.ResponseWriter, r *http.Request) {
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

	entries, err := s.store.ListEntries(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "beleg: entries", err)
		return
	}
	cost, hours, err := s.store.NeighborTotal(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		s.serverError(w, "beleg: total", err)
		return
	}
	ledger, err := s.store.ListNeighborLedger(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, "beleg: ledger", err)
		return
	}
	ledgerSum := decimal.Zero
	for _, l := range ledger {
		if !l.Voided {
			ledgerSum = ledgerSum.Add(l.Amount)
		}
	}

	// Basis items (ordered lists + id lookups) for the Kostengrundlage. Errors
	// are tolerated: without them the appendix simply stays empty.
	tractorList, _ := s.store.ListTractors(r.Context(), year.BaseID)
	loadList, _ := s.store.ListLoadLevels(r.Context(), year.BaseID)
	machineList, _ := s.store.ListMachines(r.Context(), year.BaseID)
	tractorByID := map[int64]models.Tractor{}
	for _, t := range tractorList {
		tractorByID[t.ID] = t
	}
	loadByID := map[int64]models.LoadLevel{}
	for _, l := range loadList {
		loadByID[l.ID] = l
	}
	machineByID := map[int64]models.Machine{}
	for _, m := range machineList {
		machineByID[m.ID] = m
	}

	// Collect what THIS neighbor actually used this year: which tractors, at
	// which load levels, and which machines with each — plus the set of machines
	// used overall. Voided bookings don't count.
	usedTractor := map[int64]map[int64]map[int64]bool{} // tractor -> load -> set(machine)
	usedMachine := map[int64]bool{}
	// One batched lookup of every booking's machines (avoids a per-entry query).
	// Best-effort: on error the appendix simply omits the machine links.
	machineIDsByEntry, _ := s.store.EntryMachineIDsByNeighborYear(r.Context(), neighbor.ID, year.ID)
	bookings := 0
	for _, e := range entries {
		if e.Voided {
			continue
		}
		bookings++
		if e.TractorID == nil || e.LoadLevelID == nil {
			continue
		}
		if _, ok := tractorByID[*e.TractorID]; !ok {
			continue
		}
		if _, ok := loadByID[*e.LoadLevelID]; !ok {
			continue
		}
		loads := usedTractor[*e.TractorID]
		if loads == nil {
			loads = map[int64]map[int64]bool{}
			usedTractor[*e.TractorID] = loads
		}
		set := loads[*e.LoadLevelID]
		if set == nil {
			set = map[int64]bool{}
			loads[*e.LoadLevelID] = set
		}
		for _, mid := range machineIDsByEntry[e.ID] {
			if _, ok := machineByID[mid]; ok {
				set[mid] = true
				usedMachine[mid] = true
			}
		}
	}

	// Build the two tables in the basis' own order (tractors, load levels,
	// machines), keeping only what was used.
	var gTractors []BelegTractor
	for _, t := range tractorList {
		loads := usedTractor[t.ID]
		if loads == nil {
			continue
		}
		ident := t.Ident
		if ident == "" {
			ident = t.Name
		}
		bt := BelegTractor{Ident: ident, PS: deu(t.PS)}
		for _, l := range loadList {
			set := loads[l.ID]
			if set == nil {
				continue
			}
			btl := BelegTractorLoad{Load: l.Name, CostPS: deu2(l.CostPerPS), Rate: calc.TractorRate(t, l)}
			for _, m := range machineList {
				if set[m.ID] {
					btl.Machines = append(btl.Machines, m.Name)
				}
			}
			bt.Loads = append(bt.Loads, btl)
		}
		if len(bt.Loads) > 0 {
			gTractors = append(gTractors, bt)
		}
	}
	var gMachines []BelegMachine
	for _, m := range machineList {
		if usedMachine[m.ID] {
			gMachines = append(gMachines, BelegMachine{Name: m.Name, Width: deu(m.WorkingWidth), CostAB: deu2(m.CostPerAB), Rate: calc.MachineRate(m)})
		}
	}

	// Group bookings by day (date shown once per day) and aggregate identical
	// Leistungen for the optional "Bündeln" view. Both render the same bookings as
	// the flat list; totals are unchanged (voided bookings are excluded from the
	// aggregate, exactly as they are from the totals). Entries arrive date-ordered.
	var days []BelegDay
	for _, e := range entries {
		d := e.Date.Format("02.01.")
		if n := len(days); n > 0 && days[n-1].Date == d {
			days[n-1].Entries = append(days[n-1].Entries, e)
		} else {
			days = append(days, BelegDay{Date: d, Entries: []models.Entry{e}})
		}
	}
	svcByLabel := map[string]*BelegService{}
	var svcOrder []string
	for _, e := range entries {
		if e.Voided {
			continue
		}
		// Same precedence as the flat view: task label, else the booking note,
		// else "Sonstige" — so the grouped view labels match.
		label := e.TaskLabel
		if label == "" {
			label = e.Note
		}
		if label == "" {
			label = "Sonstige"
		}
		g := svcByLabel[label]
		if g == nil {
			g = &BelegService{Label: label}
			svcByLabel[label] = g
			svcOrder = append(svcOrder, label)
		}
		g.Count++
		g.Hours = g.Hours.Add(e.Hours)
		g.Cost = g.Cost.Add(e.Cost)
	}
	groups := make([]BelegService, 0, len(svcOrder))
	for _, l := range svcOrder {
		groups = append(groups, *svcByLabel[l])
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Cost.GreaterThan(groups[j].Cost) })

	// Payments toward this year and the resulting open balance (saldo − paid).
	payments, err := s.store.ListPayments(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, "beleg: payments", err)
		return
	}
	paidSum := decimal.Zero
	for _, p := range payments {
		paidSum = paidSum.Add(p.Amount)
	}
	saldo := cost.Add(ledgerSum)
	remaining := saldo.Sub(paidSum)
	paid := !remaining.IsPositive() // fully settled

	// Invoice (Rechnung) mode: sender settings + the issued number (if any).
	company, _ := s.store.GetCompany(r.Context())
	invoice, invErr := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	hasInvoice := invErr == nil
	// Document history (invoice, its storno, credit notes) and the total credited by
	// active Gutschriften (their gross is stored negative). Not best-effort: a lookup
	// failure would understate the credited amount and thus overstate what is still
	// to pay, so surface it rather than render a wrong total.
	documents, err := s.store.ListInvoiceDocuments(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		s.serverError(w, "beleg: documents", err)
		return
	}
	var invCredits decimal.Decimal // negative: sum of active credit-note gross
	for _, d := range documents {
		if d.Kind == "gutschrift" && d.Status == "issued" && d.Content != nil {
			invCredits = invCredits.Add(d.Content.Gross)
		}
	}

	data := s.newPage(w, r, neighbor.Name+" · Beleg", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, "beleg: year selector", err)
		return
	}
	data["Neighbor"] = neighbor
	data["Days"] = days
	data["Groups"] = groups
	data["CanBundle"] = len(groups) > 0 && len(groups) < bookings
	data["Bundle"] = r.URL.Query().Get("bundeln") == "1"
	data["TotalCost"] = cost
	data["TotalHours"] = hours
	data["Ledger"] = ledger
	data["LedgerSum"] = ledgerSum
	data["Saldo"] = saldo
	data["Completed"] = year.Completed()
	data["Paid"] = paid
	data["Payments"] = payments
	data["PaidSum"] = paidSum
	data["Remaining"] = remaining
	data["HasPayments"] = len(payments) > 0
	data["Company"] = company
	data["HasInvoice"] = hasInvoice
	data["Invoice"] = invoice
	data["Rechnung"] = hasInvoice && r.URL.Query().Get("rechnung") == "1"
	// Invoice reconciliation. USt is computed on the Leistungsentgelt (the
	// services actually supplied) — NOT on the mutual-claim-netted saldo — so
	// the tax base stays correct regardless of any Verrechnung. The Verrechnung
	// and payments already received are then shown as settlement lines that
	// reduce the amount still to pay. USt is shown for §22 (pauschal) and regel,
	// not for Kleinunternehmer.
	invShowVAT := (company.TaxMode == "pauschal" || company.TaxMode == "regel") && company.VATRate.IsPositive()
	invRate := company.VATRate
	invNet := cost
	var invUSt decimal.Decimal
	if invShowVAT {
		rate := invRate.Div(decimal.NewFromInt(100))
		invUSt = invNet.Mul(rate).Round(2)
	}
	invBrutto := invNet.Add(invUSt)
	// Festschreibung: once a Rechnung is issued, its substance is frozen. Render the
	// document (net/USt/brutto/rate) from the immutable snapshot instead of a live
	// recompute; the settlement side (ledger, payments, remaining) stays live below.
	// At issuance/backfill the snapshot equals the live values, so this changes no
	// displayed amount — it only keeps the document stable if bookings change later.
	// §11 legal fields (parties, tax note): an issued invoice shows them as frozen
	// at issuance, not current company/neighbor data, so editing Betriebsdaten or a
	// neighbor address later never alters an already-issued document. Legacy invoices
	// without a snapshot fall back to the live values.
	invIssuer := models.InvoiceParty{Name: company.Name, Address: company.Address, TaxID: company.TaxID, IBAN: company.IBAN}
	invRecipient := models.InvoiceParty{Name: neighbor.Name, Address: neighbor.Address, TaxID: neighbor.TaxID}
	invTaxNote := company.TaxNote
	invIBAN := company.IBAN // live for a draft or a legacy invoice without a snapshot
	if hasInvoice && invoice.Content != nil {
		c := invoice.Content
		invShowVAT = c.ShowVAT
		invRate = c.VATRate
		invNet = c.Net
		invUSt = c.VATAmount
		invBrutto = c.Gross
		invIssuer = c.Issuer
		invRecipient = c.Recipient
		invTaxNote = c.TaxNote
		invIBAN = c.Issuer.IBAN // frozen at issuance
	}
	data["InvIssuer"] = invIssuer
	data["InvRecipient"] = invRecipient
	data["InvTaxNote"] = invTaxNote
	data["InvIBAN"] = invIBAN
	// USt share contained in the (gross) payments already received, at the
	// invoice's rate.
	var invPaidUSt decimal.Decimal
	if invShowVAT && invRate.IsPositive() {
		rate := invRate.Div(decimal.NewFromInt(100))
		invPaidUSt = paidSum.Mul(rate).Div(decimal.NewFromInt(1).Add(rate)).Round(2)
	}
	data["InvShowVAT"] = invShowVAT
	data["InvRate"] = invRate
	data["InvNet"] = invNet
	data["InvUSt"] = invUSt
	data["InvBrutto"] = invBrutto
	data["InvLedger"] = ledgerSum
	data["InvPaidUSt"] = invPaidUSt
	data["Documents"] = documents
	data["HasDocuments"] = len(documents) > 1 // more than the invoice itself
	data["InvCredits"] = invCredits           // negative sum of credit notes
	data["HasCredits"] = invCredits.IsNegative()
	// Amount still to pay: gross services, less credit notes, less mutual
	// Verrechnung, less payments. invCredits is negative, so it reduces the total;
	// it is zero without any Gutschrift, leaving the previous value unchanged.
	data["InvRest"] = invBrutto.Add(invCredits).Add(ledgerSum).Sub(paidSum)
	// § 11: the recipient's UID/tax number is required on invoices over €10,000
	// gross. Soft reminder only — it never blocks issuing.
	data["InvNeedRecipientVATID"] = invBrutto.GreaterThan(decimal.NewFromInt(10000)) &&
		strings.TrimSpace(invRecipient.TaxID) == ""
	data["GrundTractors"] = gTractors
	data["GrundMachines"] = gMachines
	data["HasGrund"] = len(gTractors) > 0 || len(gMachines) > 0
	data["Bookings"] = bookings
	data["ShowGrund"] = r.URL.Query().Get("grundlage") == "1"
	data["Today"] = time.Now().Format("02.01.2006")
	s.render(w, r, "beleg", data)
}

// handleInvoiceIssue assigns and stores a sequential invoice number for a
// neighbor+year (fixed once), then shows the Beleg in Rechnung mode.
func (s *Server) handleInvoiceIssue(w http.ResponseWriter, r *http.Request) {
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
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		s.serverError(w, "invoice: year", err)
		return
	}
	member, err := s.store.NeighborInYear(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "invoice: membership", err)
		return
	}
	if !member {
		s.setFlash(w, r, "error", "Nachbar ist in diesem Abrechnungsjahr nicht vorhanden.")
		redirect(w, r, neighborURL(neighborID, yearID))
		return
	}
	// A formal Rechnung needs a sender: don't fix an invoice number against empty
	// Betriebsdaten — send the user to fill them in first.
	if company, err := s.store.GetCompany(r.Context()); err != nil || strings.TrimSpace(company.Name) == "" {
		s.setFlash(w, r, "error", "Bitte zuerst die Betriebsdaten (Absender) ausfüllen.")
		redirect(w, r, fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID))
		return
	}
	// § 11 UStG: only fix a number once every mandatory field is present. Build the
	// content that will be frozen and block issuance if anything is missing, listing
	// exactly what to fix. Skipped for an already-issued invoice (idempotent re-issue).
	if _, err := s.store.GetInvoice(r.Context(), yearID, neighborID); errors.Is(err, store.ErrNotFound) {
		content, err := s.store.BuildInvoiceContent(r.Context(), yearID, neighborID)
		if err != nil {
			s.serverError(w, "invoice: build content", err)
			return
		}
		if missing := content.MissingMandatory(); len(missing) > 0 {
			s.setFlash(w, r, "error", "Rechnung unvollständig (§ 11 UStG). Bitte ergänzen: "+strings.Join(missing, ", ")+".")
			redirect(w, r, fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID))
			return
		}
	} else if err != nil {
		s.serverError(w, "invoice: lookup", err)
		return
	}
	iv, err := s.store.IssueInvoice(r.Context(), yearID, neighborID, year.Year)
	if err != nil {
		s.setFlash(w, r, "error", "Rechnung konnte nicht ausgestellt werden.")
		redirect(w, r, fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID))
		return
	}
	s.audit(r, "invoice_issue", "neighbor", neighborID, s.neighborName(r, neighborID)+" · Rechnung "+iv.Number)
	s.setFlash(w, r, "success", "Rechnung "+iv.Number+" ausgestellt.")
	redirect(w, r, fmt.Sprintf("/neighbors/%d/beleg?year=%d&rechnung=1", neighborID, yearID))
}

// handleInvoiceStorno cancels the active invoice (issues a Storno document and
// marks the original canceled), which unlocks the neighbor's bookings again.
func (s *Server) handleInvoiceStorno(w http.ResponseWriter, r *http.Request) {
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
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID)
	sv, err := s.store.StornoInvoice(r.Context(), yearID, neighborID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Keine aktive Rechnung zum Stornieren.")
	case err != nil:
		s.setFlash(w, r, "error", "Storno fehlgeschlagen.")
	default:
		s.audit(r, "invoice_storno", "neighbor", neighborID, s.neighborName(r, neighborID)+" · Storno "+sv.Number)
		s.setFlash(w, r, "success", "Rechnung storniert ("+sv.Number+"). Die Buchungen sind wieder bearbeitbar.")
	}
	redirect(w, r, back)
}

// handleInvoiceGutschrift issues a credit note (§ 16 UStG Entgeltminderung, e.g. a
// Skonto) reducing the active invoice by a gross amount. The original stays active.
func (s *Server) handleInvoiceGutschrift(w http.ResponseWriter, r *http.Request) {
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
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d&rechnung=1", neighborID, yearID)
	note := trimmed(r, "note")
	if s.tooLong(w, r, "Grund", note, maxNoteLen) {
		redirect(w, r, back)
		return
	}
	gv, err := s.store.GutschriftInvoice(r.Context(), yearID, neighborID, formDecimal(r, "amount").Abs(), note)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Keine aktive Rechnung für eine Gutschrift.")
	case errors.Is(err, store.ErrGutschriftTooLarge):
		s.setFlash(w, r, "error", "Die Gutschrift übersteigt den offenen Rechnungsbetrag.")
	case err != nil:
		s.setFlash(w, r, "error", "Gutschrift fehlgeschlagen – bitte einen Betrag größer 0 angeben.")
	default:
		s.audit(r, "invoice_gutschrift", "neighbor", neighborID, s.neighborName(r, neighborID)+" · Gutschrift "+gv.Number)
		s.setFlash(w, r, "success", "Gutschrift "+gv.Number+" erstellt.")
	}
	redirect(w, r, back)
}
