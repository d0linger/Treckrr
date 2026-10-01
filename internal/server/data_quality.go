package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/d0linger/treckrr/internal/models"
)

// qualityIssueView adds operator copy and a direct correction target to a store
// finding. The checks remain advisory and never block an existing workflow.
type qualityIssueView struct {
	Severity string
	Group    string
	Title    string
	Detail   string
	Href     string
}

// dataQualityView is the selected year's completeness report.
type dataQualityView struct {
	Issues         []qualityIssueView
	NeighborCounts map[int64]int
	High           int
	Medium         int
}

// loadDataQuality converts stable finding codes into concise German guidance.
func (s *Server) loadDataQuality(r *http.Request, year *models.BillingYear) (dataQualityView, error) {
	report, err := s.store.YearDataQuality(r.Context(), year.ID, year.BaseID)
	if err != nil {
		return dataQualityView{}, err
	}
	out := dataQualityView{Issues: make([]qualityIssueView, 0), NeighborCounts: report.NeighborIssueCounts()}
	for _, issue := range report.Issues {
		view := qualityIssueView{Severity: issue.Severity, Group: "Grundlage", Href: fmt.Sprintf("/prices?base=%d", year.BaseID)}
		switch issue.Code {
		case "neighbor_address":
			view.Group, view.Title = "Nachbar", issue.Subject+": Adresse fehlt"
			view.Detail = "Eine Rechnung kann ohne Empfängeradresse nicht festgeschrieben werden."
			view.Href = neighborURL(issue.NeighborID, year.ID)
		case "neighbor_email":
			view.Group, view.Title = "Nachbar", issue.Subject+": E-Mail fehlt"
			view.Detail = "Belege können nur manuell übergeben werden."
			view.Href = neighborURL(issue.NeighborID, year.ID)
		case "neighbor_iban":
			view.Group, view.Title = "Nachbar", issue.Subject+": IBAN fehlt"
			view.Detail = "Der Zahlungsimport kann diesen Nachbarn nicht anhand des Kontos zuordnen."
			view.Href = neighborURL(issue.NeighborID, year.ID)
		case "catalog_tractors":
			view.Title, view.Detail = "Keine aktiven Traktoren", "Maschinenbuchungen können nicht vollständig kalkuliert werden."
		case "catalog_load_levels":
			view.Title, view.Detail = "Keine Belastungsstufen", "Traktorkosten benötigen mindestens eine Belastungsstufe."
		case "catalog_machines":
			view.Title, view.Detail = "Keine aktiven Maschinen", "Die Grundlage enthält keine verwendbare Maschine."
		case "machine_rate":
			view.Title, view.Detail = issue.Subject+": Preissatz unvollständig", "Arbeitsbreite und Preis je AB·h müssen positiv sein."
			view.Href += fmt.Sprintf("#machine-%d", issue.EntityID)
		case "machine_self_cost":
			view.Title, view.Detail = issue.Subject+": Selbstkosten fehlen", "Deckungsbeitrag und Eigenkosten bleiben für neue Buchungen unvollständig."
			view.Href += fmt.Sprintf("#machine-%d", issue.EntityID)
		default:
			continue
		}
		out.add(view)
	}

	company, err := s.store.GetCompany(r.Context())
	if err != nil {
		return dataQualityView{}, err
	}
	companyHref := ""
	if user := userFromCtx(r); user != nil && user.IsAdmin {
		companyHref = "/admin/company"
	}
	addCompany := func(severity, title, detail string) {
		out.add(qualityIssueView{Severity: severity, Group: "Betrieb", Title: title, Detail: detail, Href: companyHref})
	}
	if strings.TrimSpace(company.Name) == "" {
		addCompany("high", "Betriebsname fehlt", "Der Absender eines Belegs ist unvollständig.")
	}
	if strings.TrimSpace(company.Address) == "" {
		addCompany("high", "Betriebsadresse fehlt", "Rechnungen benötigen eine vollständige Absenderadresse.")
	}
	switch company.TaxMode {
	case "regel":
		if !company.VATRate.IsPositive() {
			addCompany("high", "Umsatzsteuersatz fehlt", "Regelbesteuerung benötigt einen positiven Steuersatz.")
		}
	case "pauschal":
		if strings.TrimSpace(company.TaxNote) == "" {
			addCompany("high", "Steuerhinweis fehlt", "Pauschalierung benötigt den Hinweistext am Beleg.")
		}
	default:
		addCompany("high", "Steuermodus unvollständig", "Pauschalierung oder Regelbesteuerung auswählen.")
	}
	if strings.TrimSpace(company.IBAN) == "" {
		addCompany("medium", "Betriebs-IBAN fehlt", "Zahlungs-QR und Überweisungshinweis bleiben ohne Empfängerkonto.")
	}
	return out, nil
}

// add appends an issue and maintains severity totals for dashboard summaries.
func (v *dataQualityView) add(issue qualityIssueView) {
	v.Issues = append(v.Issues, issue)
	if issue.Severity == "high" {
		v.High++
	} else {
		v.Medium++
	}
}

// handleDataQuality renders every finding instead of truncating the dashboard
// work item, preserving a direct path to each missing value.
func (s *Server) handleDataQuality(w http.ResponseWriter, r *http.Request) {
	year, ok := s.resolveYear(w, r)
	if !ok {
		return
	}
	quality, err := s.loadDataQuality(r, year)
	if err != nil {
		s.serverError(w, "data quality", err)
		return
	}
	data := s.newPage(w, r, "Datenqualität", "dashboard")
	if err := s.withYearSelector(r, data, year); err != nil {
		s.serverError(w, "data quality: year selector", err)
		return
	}
	data["Quality"] = quality
	s.render(w, r, "data_quality", data)
}
