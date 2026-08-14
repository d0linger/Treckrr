package server

import (
	"net/http"
	"strings"
	"time"
)

// dunningStage maps a stage code to its German document heading + intro line.
// Stage 0 = friendly reminder (no fee), 1/2 = escalating Mahnungen. Chosen at
// print time; no state is persisted.
func dunningStage(stage int) (title, intro string) {
	switch stage {
	case 1:
		return "1. Mahnung", "Trotz unserer Zahlungserinnerung ist der folgende Betrag noch offen. Wir bitten Sie, den Ausgleich umgehend vorzunehmen."
	case 2:
		return "2. Mahnung", "Der folgende Betrag ist weiterhin offen. Bitte begleichen Sie ihn unverzüglich, um weitere Schritte zu vermeiden."
	default:
		return "Zahlungserinnerung", "Vermutlich haben Sie es übersehen – der folgende Betrag ist noch offen. Bitte gleichen Sie ihn bei Gelegenheit aus."
	}
}

// handleMahnwesen renders the dunning list: neighbors in the selected billing
// year whose issued invoice is unpaid and past due (issue date + payment term).
func (s *Server) handleMahnwesen(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	term := company.PaymentTermDays
	if term <= 0 {
		term = 14
	}
	rows, err := s.store.DunningRows(r.Context(), year.ID, term, time.Now())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}

	data := s.newPage(w, r, "Mahnwesen", "mahnwesen")
	data["Year"] = year
	data["Rows"] = rows
	data["Term"] = term
	s.render(w, r, "mahnwesen", data)
}

// handleNeighborMahnung renders a printable reminder/dunning letter for one
// neighbor. The stage (0 = Zahlungserinnerung, 1/2 = Mahnungen) is chosen at
// print time and sets the heading and wording — no state is persisted.
func (s *Server) handleNeighborMahnung(w http.ResponseWriter, r *http.Request) {
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
	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	net, paid, err := s.store.NeighborNetPaid(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	open := net.Sub(paid)

	// formInt parses to a platform int via strconv.Atoi (no lossy int64->int
	// narrowing), which also satisfies CodeQL's integer-conversion check. Unknown
	// values fall through dunningStage's default (Zahlungserinnerung).
	stage := formInt(r, "stufe")
	title, intro := dunningStage(stage)

	term := company.PaymentTermDays
	if term <= 0 {
		term = 14
	}
	var issued time.Time
	invNo := ""
	if iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID); err == nil {
		invNo = iv.Number
		issued = iv.IssuedOn
	}

	data := s.newPage(w, r, title, "")
	data["Neighbor"] = neighbor
	data["Year"] = year
	data["Today"] = time.Now()
	data["Company"] = company
	data["Title"] = title
	data["Intro"] = intro
	data["Stage"] = stage
	data["Open"] = open
	data["Paid"] = paid
	data["Net"] = net
	data["InvoiceNo"] = invNo
	data["IssuedOn"] = issued
	if !issued.IsZero() {
		data["DueOn"] = issued.AddDate(0, 0, term)
	}
	data["HasEpcQR"] = strings.TrimSpace(company.IBAN) != "" && open.IsPositive()
	s.render(w, r, "mahnung", data)
}

// handleMahnungEpcQR serves the EPC/GiroCode QR for a reminder, encoding the
// currently OPEN amount (not the full invoice gross) so a scan pays exactly what
// is outstanding.
func (s *Server) handleMahnungEpcQR(w http.ResponseWriter, r *http.Request) {
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
	company, err := s.store.GetCompany(r.Context())
	if err != nil || strings.TrimSpace(company.IBAN) == "" {
		http.NotFound(w, r)
		return
	}
	net, paid, err := s.store.NeighborNetPaid(r.Context(), yearID, neighborID)
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	open := net.Sub(paid)
	if !open.IsPositive() {
		http.NotFound(w, r)
		return
	}
	ref := ""
	if iv, err := s.store.GetInvoice(r.Context(), yearID, neighborID); err == nil {
		ref = iv.Number
	}
	png, err := qrPNG(epcPayload(company.Name, company.IBAN, open, ref))
	if err != nil {
		s.serverError(w, r.URL.Path, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}
