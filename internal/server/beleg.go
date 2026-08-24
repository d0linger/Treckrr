package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/auth"
	"github.com/d0linger/treckrr/internal/calc"
	"github.com/d0linger/treckrr/internal/mail"
	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/pdf"
	"github.com/d0linger/treckrr/internal/store"
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

// buildBelegData assembles the full Beleg view model for a neighbor+year: the
// bookings table, totals, ledger, payments, invoice/snapshot state, EPC-QR and the
// Kostengrundlage appendix. Shared by the authenticated Beleg page and the public
// share link so both render the identical document.
func (s *Server) buildBelegData(w http.ResponseWriter, r *http.Request, neighbor *models.Neighbor, year *models.BillingYear) (pageData, error) {
	entries, err := s.store.ListEntries(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		return nil, err
	}
	cost, hours, err := s.store.NeighborTotal(r.Context(), neighbor.ID, year.ID)
	if err != nil {
		return nil, err
	}
	ledger, err := s.store.ListNeighborLedger(r.Context(), year.ID, neighbor.ID)
	if err != nil {
		return nil, err
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
		return nil, err
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
		return nil, err
	}
	var invCredits decimal.Decimal // negative: sum of active credit-note gross
	for _, d := range documents {
		if d.Kind == "gutschrift" && d.Status == "issued" && d.Content != nil {
			invCredits = invCredits.Add(d.Content.Gross)
		}
	}

	data := s.newPage(w, r, neighbor.Name+" · Beleg", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		return nil, err
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
	// Integrity anchor of the frozen snapshot (shown short); empty for a legacy
	// invoice without a snapshot.
	if hasInvoice && invoice.Content != nil && invoice.Content.Hash != "" {
		h := invoice.Content.Hash
		if len(h) > 12 {
			h = h[:8] + "…" + h[len(h)-4:]
		}
		data["InvHash"] = h
	}
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
	// Amount still to pay: gross services, less credit notes (invCredits is
	// negative), less mutual Verrechnung, less payments.
	invRest := invBrutto.Add(invCredits).Add(ledgerSum).Sub(paidSum)
	// Scan-to-pay: show the EPC/GiroCode QR only for an issued invoice with an
	// issuer IBAN and a positive REMAINING amount (the /epc-qr.png endpoint encodes
	// that same remaining, so a partial payment/credit is reflected in the QR).
	data["HasEpcQR"] = hasInvoice && strings.TrimSpace(invIBAN) != "" && invRest.IsPositive()
	data["InvLedger"] = ledgerSum
	data["InvPaidUSt"] = invPaidUSt
	data["Documents"] = documents
	data["HasDocuments"] = len(documents) > 1 // more than the invoice itself
	data["InvCredits"] = invCredits           // negative sum of credit notes
	data["HasCredits"] = invCredits.IsNegative()
	data["InvRest"] = invRest
	// Due date + countdown for the invoice: same definition as the Mahnwesen list
	// (issue date + company payment term). Only meaningful while something is still
	// payable; the template shows "fällig am … (in N Tagen / seit N Tagen überfällig)".
	if hasInvoice && !invoice.IssuedOn.IsZero() && invRest.IsPositive() {
		term := company.PaymentTermDays
		if term < 0 {
			term = 14
		}
		due := invoice.IssuedOn.AddDate(0, 0, term)
		data["DueOn"] = due
		// Whole-day, DST-safe difference (shared with the dunning overdue count).
		days := calc.DaysBetween(time.Now(), due)
		data["DueDays"] = days // >0 days left, 0 today, <0 overdue
		if days < 0 {
			data["OverdueDays"] = -days
		}
	}
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
	return data, nil
}

// handleNeighborBeleg renders the authenticated Beleg page for one neighbor+year.
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
	data, err := s.buildBelegData(w, r, neighbor, year)
	if err != nil {
		s.serverError(w, "beleg", err)
		return
	}
	data["LastSend"], _ = s.store.LastBelegSend(r.Context(), year.ID, neighbor.ID) // best-effort marker
	data["MailEnabled"] = s.cfg.MailEnabled()
	data["NeighborEmail"] = neighbor.Email
	// A freshly minted share link arrives via the one-time cookie (never a URL);
	// read-and-clear so the raw token is shown exactly once.
	if c, err := s.cookie(r, shareOnceCookie); err == nil && c.Value != "" && len(c.Value) <= maxBelegShareTokenLen {
		s.setCookie(w, r, &http.Cookie{Name: shareOnceCookie, Value: "", MaxAge: -1})
		data["ShareToken"] = c.Value
		data["ShareURL"] = s.absoluteURL(r, "/s/beleg/"+c.Value)
	}
	// Self-service: list the Beleg's active public links (manage/revoke).
	if hasInv, _ := data["HasInvoice"].(bool); hasInv {
		data["Shares"], _ = s.store.ListBelegShares(r.Context(), neighbor.ID, year.ID) // best-effort
		data["ShareDayOptions"] = belegShareDayOptions
		data["ShareDefaultDays"] = belegShareDefaultDays
	}
	s.render(w, r, "beleg", data)
}

// Share tokens reuse the session-token primitives (Parkrr portal-link
// pattern): auth.NewToken mints the random RAW credential (hex → dot-free by
// construction; the dot marks legacy HMAC tokens) and store.HashToken puts
// only its SHA-256 hex into beleg_shares — a DB leak exposes no usable links.

// belegShareDayOptions is the single source of truth for the offered
// validities (Linkdauer): it renders the <select> AND validates the submitted
// value, so template and handler can never diverge.
var belegShareDayOptions = []int{7, 30, 90}

const belegShareDefaultDays = 30

func belegShareDays(v string) int {
	n, _ := strconv.Atoi(v)
	for _, d := range belegShareDayOptions {
		if n == d {
			return d
		}
	}
	return belegShareDefaultDays
}

// maxBelegShareTokenLen bounds the raw share token input so an oversized payload
// cannot drive unnecessary memory allocations or HMAC hashing on the public route (DoS defense).
const maxBelegShareTokenLen = 200

// shareOnceCookie carries a freshly minted raw share token from the create
// redirect to exactly one authenticated render — instead of a ?share= query
// parameter, which would park a bearer credential in browser history and any
// URL-shaped sink. Read-and-cleared on first use; short-lived either way.
const shareOnceCookie = "treckrr_share_once"

// verifyLegacyBelegShare validates a pre-0038 HMAC share token (signature +
// baked-in expiry). Kept so links minted before the DB-backed scheme keep
// working until they expire; new tokens are dot-free and resolve via the DB.
func (s *Server) verifyLegacyBelegShare(token string) (neighborID, yearID int64, ok bool) {
	if len(token) > maxBelegShareTokenLen {
		return 0, 0, false
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return 0, 0, false
	}
	payload := string(pb)
	mac := hmac.New(sha256.New, []byte(s.cfg.SessionSecret))
	mac.Write([]byte("belegshare:" + payload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(parts[1])) {
		return 0, 0, false
	}
	var exp int64
	if _, err := fmt.Sscanf(payload, "%d:%d:%d", &neighborID, &yearID, &exp); err != nil {
		return 0, 0, false
	}
	if time.Now().Unix() > exp {
		return 0, 0, false
	}
	return neighborID, yearID, true
}

// handleBelegShareCreate mints a public link for a neighbor's Beleg — only once
// the invoice is festgeschrieben, so a public URL never exposes draft/live data.
func (s *Server) handleBelegShareCreate(w http.ResponseWriter, r *http.Request) {
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
	if _, err := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID); errors.Is(err, store.ErrNotFound) {
		s.setFlash(w, r, "error", "Ein öffentlicher Link ist erst nach dem Festschreiben der Rechnung möglich.")
		redirect(w, r, "/neighbors/"+itoa64(id)+"/beleg?year="+itoa64(year.ID))
		return
	} else if err != nil {
		s.serverError(w, "share create: invoice lookup", err)
		return
	}
	// Self-service: the owner picks the validity; the raw token exists only in
	// this redirect (the DB keeps just its hash).
	days := belegShareDays(strings.TrimSpace(r.FormValue("days")))
	raw, err := auth.NewToken()
	if err != nil {
		s.serverError(w, "share create: token", err)
		return
	}
	hash := store.HashToken(raw)
	expires := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	createdBy := ""
	if u := userFromCtx(r); u != nil { // behind s.auth — no need to re-resolve the session
		createdBy = u.Username
	}
	if _, err := s.store.CreateBelegShare(r.Context(), hash, neighbor.ID, year.ID, expires, createdBy); err != nil {
		s.serverError(w, "share create: store", err)
		return
	}
	s.audit(r, "beleg_share", "neighbor", neighbor.ID,
		neighbor.Name+" · "+s.yearLabel(r, year.ID)+" · gültig "+itoa64(int64(days))+" Tage (bis "+expires.Format("02.01.2006")+")")
	// One-time cookie, not a query parameter: the raw token must never enter a
	// URL (browser history, Referer). setCookie enforces HttpOnly + Lax.
	s.setCookie(w, r, &http.Cookie{Name: shareOnceCookie, Value: raw, MaxAge: 60})
	redirect(w, r, "/neighbors/"+itoa64(id)+"/beleg?year="+itoa64(year.ID))
}

// handleBelegShareRevoke deletes (revokes) one public link — the self-service
// counterpart to create. Idempotent; scoped to the neighbor in the path.
func (s *Server) handleBelegShareRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	shareID := formInt64(r, "share_id")
	if shareID <= 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	revoked, err := s.store.RevokeBelegShare(r.Context(), shareID, id, year.ID)
	if err != nil {
		s.serverError(w, "share revoke", err)
		return
	}
	if revoked {
		s.audit(r, "beleg_share_revoke", "neighbor", id, s.neighborName(r, id)+" · "+s.yearLabel(r, year.ID)+" · Link gelöscht")
		s.setFlash(w, r, "success", "Öffentlicher Link gelöscht — er ist ab sofort ungültig.")
	} else {
		s.setFlash(w, r, "info", "Der Link war bereits gelöscht oder abgelaufen.")
	}
	redirect(w, r, "/neighbors/"+itoa64(id)+"/beleg?year="+itoa64(year.ID))
}

// handleSharedBeleg renders the read-only public Beleg for a valid share token.
// Unauthenticated (so the layout drops all chrome) and gated on a festgeschriebene
// invoice; anything else 404s so a token can't probe live data.
func (s *Server) handleSharedBeleg(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if len(token) > maxBelegShareTokenLen {
		http.NotFound(w, r)
		return
	}
	// The only unauthenticated route that touches the database, and it runs
	// several queries per hit. Throttle by counting misses (see shareMaxMisses):
	// a visitor with a working link is never counted, a scanner is cut off after a
	// handful of dead tokens. The response is the same 404 either way, so being
	// throttled is not distinguishable from a bad token.
	clientIP := s.clientIP(r)
	if s.logins.shareBlocked(r.Context(), clientIP) {
		http.NotFound(w, r)
		return
	}
	var nID, yID int64
	var ok bool
	if strings.Contains(token, ".") {
		// Legacy pre-0038 HMAC token — honored until it expires. Every legacy
		// token was minted with a fixed 30-day validity, so this branch is dead
		// 30 days after 0038 reaches production.
		// TODO: delete this branch, verifyLegacyBelegShare and its oversized-
		// token test once all pre-0038 links have expired (deploy date + 30 d).
		nID, yID, ok = s.verifyLegacyBelegShare(token)
	} else {
		var err error
		nID, yID, ok, err = s.store.ResolveBelegShare(r.Context(), store.HashToken(token))
		if err != nil {
			s.serverError(w, "shared beleg: resolve", err)
			return
		}
	}
	if !ok {
		s.logins.shareMiss(r.Context(), clientIP)
		http.NotFound(w, r)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), nID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if _, err := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID); err != nil {
		http.NotFound(w, r) // not festgeschrieben (or gone) → no public view
		return
	}
	data, err := s.buildBelegData(w, r, neighbor, year)
	if err != nil {
		s.serverError(w, "shared beleg", err)
		return
	}
	data["Shared"] = true
	w.Header().Set("Cache-Control", "no-store") // per-neighbor PII — don't cache
	s.render(w, r, "beleg", data)
}

// handleBelegPDF serves the festgeschriebene Rechnung as a server-rendered PDF —
// consistent and archivable regardless of the viewer's browser. Only for an issued
// invoice (which has a frozen snapshot to render from).
func (s *Server) handleBelegPDF(w http.ResponseWriter, r *http.Request) {
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
	iv, err := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	if err != nil || iv.Content == nil {
		s.setFlash(w, r, "error", "Ein PDF gibt es erst nach dem Festschreiben der Rechnung.")
		redirect(w, r, "/neighbors/"+itoa64(id)+"/beleg?year="+itoa64(year.ID))
		return
	}
	blob, err := pdf.RenderInvoice(&iv)
	if err != nil {
		s.serverError(w, "beleg pdf", err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="Rechnung_`+sanitizeFilename(iv.Number)+`.pdf"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(blob)
}

// absoluteURL builds a full URL for path from the request, honoring a TLS proxy.
// The scheme comes from cookieSecure, which only believes X-Forwarded-Proto when
// TRUST_PROXY is on AND the direct peer is inside TRUSTED_PROXIES — the same gate
// the Secure cookie flag uses. It previously read the header unconditionally, so
// any client could flip the scheme of a rendered link by sending it.
func (s *Server) absoluteURL(r *http.Request, path string) string {
	scheme := "http"
	if r.TLS != nil || s.cookieSecure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + path
}

// handleBelegEmail sends the festgeschriebene Rechnung PDF to the neighbor's
// e-mail. Requires SMTP configured, a neighbor e-mail, and an issued invoice.
func (s *Server) handleBelegEmail(w http.ResponseWriter, r *http.Request) {
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
	back := "/neighbors/" + itoa64(id) + "/beleg?year=" + itoa64(year.ID)
	if !s.cfg.MailEnabled() {
		s.setFlash(w, r, "error", "E-Mail-Versand ist nicht konfiguriert (SMTP_HOST/SMTP_FROM).")
		redirect(w, r, back)
		return
	}
	if strings.TrimSpace(neighbor.Email) == "" {
		s.setFlash(w, r, "error", "Für "+neighbor.Name+" ist keine E-Mail-Adresse hinterlegt.")
		redirect(w, r, back)
		return
	}
	iv, err := s.store.GetInvoice(r.Context(), year.ID, neighbor.ID)
	if err != nil || iv.Content == nil {
		s.setFlash(w, r, "error", "E-Mail-Versand ist erst nach dem Festschreiben der Rechnung möglich.")
		redirect(w, r, back)
		return
	}
	blob, err := pdf.RenderInvoice(&iv)
	if err != nil {
		s.serverError(w, "beleg email: pdf", err)
		return
	}
	company, _ := s.store.GetCompany(r.Context())
	from := strings.TrimSpace(company.Name)
	if from == "" {
		from = "Ihr Maschinenring"
	}
	body := "Guten Tag " + neighbor.Name + ",\n\nanbei die Rechnung " + iv.Number + " als PDF.\n\nMit freundlichen Grüßen\n" + from
	att := mail.Attachment{Filename: "Rechnung_" + sanitizeFilename(iv.Number) + ".pdf", ContentType: "application/pdf", Data: blob}
	if err := mail.Send(s.cfg, neighbor.Email, "Rechnung "+iv.Number, body, []mail.Attachment{att}); err != nil {
		slog.Error("beleg email send failed", "neighbor", neighbor.ID, "err", sanitizeLog(err.Error()))
		s.setFlash(w, r, "error", "Versand fehlgeschlagen.")
		redirect(w, r, back)
		return
	}
	// Delivery succeeded; the send-trail marker is secondary. If recording it fails
	// don't fail the request — log it and tell the user the send worked but the
	// history entry didn't, so "zuletzt versendet am …" being absent isn't a mystery.
	if err := s.store.RecordBelegSend(r.Context(), year.ID, neighbor.ID, "e-mail"); err != nil {
		slog.Error("record beleg send failed", "year", year.ID, "neighbor", neighbor.ID, "err", err)
		s.audit(r, "beleg_email", "neighbor", neighbor.ID, neighbor.Name+" · E-Mail · Rechnung "+iv.Number)
		s.setFlash(w, r, "success", "Rechnung an "+neighbor.Email+" gesendet (Versand-Historie konnte nicht gespeichert werden).")
		redirect(w, r, back)
		return
	}
	s.audit(r, "beleg_email", "neighbor", neighbor.ID, neighbor.Name+" · E-Mail · Rechnung "+iv.Number)
	s.setFlash(w, r, "success", "Rechnung an "+neighbor.Email+" gesendet.")
	redirect(w, r, back)
}

// handleBelegMarkSent records that this neighbor's Beleg was handed over/sent, so
// the page can show "zuletzt versendet am …". Manual channel; the e-mail feature
// records its own send with channel "e-mail".
func (s *Server) handleBelegMarkSent(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.RecordBelegSend(r.Context(), year.ID, neighbor.ID, "manuell"); err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	s.audit(r, "beleg_sent", "neighbor", neighbor.ID, neighbor.Name+" · "+s.yearLabel(r, year.ID)+" (manuell)")
	// A manual mark is a reversible note — offer a one-click Undo in the toast.
	s.setFlashUndo(w, r, "success", "Als versendet markiert.",
		"/neighbors/"+itoa64(id)+"/beleg/unsend?year="+itoa64(year.ID))
	redirect(w, r, "/neighbors/"+itoa64(id)+"/beleg?year="+itoa64(year.ID))
}

// handleBelegUnsend reverses the most recent MANUAL "als versendet" mark — the Undo
// action from the mark-sent toast. It only removes a manual note, never an e-mail or
// Mahnung send record.
func (s *Server) handleBelegUnsend(w http.ResponseWriter, r *http.Request) {
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
	undone, err := s.store.DeleteLatestManualBelegSend(r.Context(), year.ID, neighbor.ID)
	switch {
	case err != nil:
		s.setFlash(w, r, "error", "Rückgängig machen fehlgeschlagen.")
	case undone:
		s.audit(r, "beleg_unsent", "neighbor", neighbor.ID, neighbor.Name+" · "+s.yearLabel(r, year.ID)+" (manuell)")
		s.setFlash(w, r, "success", "Versendet-Markierung entfernt.")
	default: // nothing manual to undo (e.g. only an e-mail send exists)
		s.setFlash(w, r, "info", "Keine manuelle Markierung zum Entfernen.")
	}
	redirect(w, r, "/neighbors/"+itoa64(id)+"/beleg?year="+itoa64(year.ID))
}

// handleInvoiceConfirm renders the pre-issuance confirmation: the § 11 checklist
// and, when complete, the frozen-snapshot preview with the festschreiben button.
func (s *Server) handleInvoiceConfirm(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID := formInt64(r, "year")
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	year, err := s.store.GetBillingYear(r.Context(), yearID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID)
	// Already issued → nothing to confirm; show the Beleg (storno/gutschrift live there).
	if _, err := s.store.GetInvoice(r.Context(), yearID, neighborID); err == nil {
		redirect(w, r, back+"&rechnung=1")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, "invoice confirm: lookup", err)
		return
	}
	neighbor, err := s.store.GetNeighbor(r.Context(), neighborID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	content, err := s.store.BuildInvoiceContent(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, "invoice confirm: build", err)
		return
	}
	data := s.newPage(w, r, neighbor.Name+" · Festschreiben", "dashboard")
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Checks"] = content.MandatoryChecks()
	data["CanIssue"] = len(content.MissingMandatory()) == 0
	data["Content"] = content
	data["BackURL"] = back
	s.render(w, r, "invoice_confirm", data)
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
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
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
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	back := fmt.Sprintf("/neighbors/%d/beleg?year=%d", neighborID, yearID)
	reason := trimmed(r, "reason")
	if s.tooLong(w, r, "Grund", reason, maxNoteLen) {
		redirect(w, r, back)
		return
	}
	sv, err := s.store.StornoInvoice(r.Context(), yearID, neighborID, reason)
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, r, "error", "Keine aktive Rechnung zum Stornieren.")
	case err != nil:
		s.setFlash(w, r, "error", "Storno fehlgeschlagen.")
	default:
		detail := s.neighborName(r, neighborID) + " · Storno " + sv.Number
		if reason != "" {
			detail += " · " + reason
		}
		s.audit(r, "invoice_storno", "neighbor", neighborID, detail)
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
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
		return
	}
	yearID := s.yearIDFromForm(r)
	if yearID == 0 {
		s.badRequest(w, "Die Anfrage konnte nicht verarbeitet werden — bitte die Seite neu laden und erneut versuchen.")
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

// handleInvoiceEpcQR serves the EPC069-12 ("GiroCode") QR as a PNG so the printed
// invoice can carry a scan-to-pay code. Only when an active invoice exists, an
// issuer IBAN is set (frozen, else live) and the gross is positive.
func (s *Server) handleInvoiceEpcQR(w http.ResponseWriter, r *http.Request) {
	neighborID, err := pathID(r)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	yearID := formInt64(r, "year")
	if yearID == 0 {
		http.Error(w, "Ungültige Anfrage", http.StatusBadRequest)
		return
	}
	iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID)
	if err != nil || iv.Content == nil {
		http.NotFound(w, r)
		return
	}
	// Encode the amount STILL PAYABLE (gross less credits/ledger/payments), not the
	// full gross — so scanning a partly-paid or credited invoice pays the right sum.
	rest, err := s.store.InvoiceRemaining(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	if !rest.IsPositive() {
		http.NotFound(w, r)
		return
	}
	iban := iv.Content.Issuer.IBAN // frozen at issuance
	if strings.TrimSpace(iban) == "" {
		if c, cerr := s.store.GetCompany(r.Context()); cerr == nil {
			iban = c.IBAN // legacy invoice without a frozen IBAN → live
		}
	}
	if strings.TrimSpace(iban) == "" {
		http.NotFound(w, r)
		return
	}
	ref := iv.PaymentReference
	if ref == "" {
		ref = iv.Number
	}
	png, err := qrPNG(epcPayload(iv.Content.Issuer.Name, iban, rest, ref))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}
