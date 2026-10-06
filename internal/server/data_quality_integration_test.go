package server

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestDataQualityAndWorkQueueIntegration verifies the selected year's advisory
// findings appear both in the central queue and in the full correction list.
func TestDataQualityAndWorkQueueIntegration(t *testing.T) {
	e := newItEnv(t)
	report, err := e.st.YearDataQuality(e.ctx, e.yearID64, e.baseID64)
	if err != nil {
		t.Fatal(err)
	}
	codes := make(map[string]bool)
	for _, issue := range report.Issues {
		codes[issue.Code] = true
	}
	for _, code := range []string{"neighbor_email", "neighbor_iban"} {
		if !codes[code] {
			t.Errorf("fixture finding %q missing: %+v", code, report.Issues)
		}
	}
	if codes["neighbor_address"] || codes["machine_rate"] || codes["machine_self_cost"] {
		t.Errorf("complete fixture fields were reported missing: %+v", report.Issues)
	}

	company, err := e.st.GetCompany(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	company.TaxMode = "kleinunternehmer"
	company.VATRate = decimal.Zero
	company.TaxNote = "Umsatzsteuerbefreit gemäß § 6 Abs. 1 Z 27 UStG."
	if err := e.st.UpdateCompany(e.ctx, company); err != nil {
		t.Fatal(err)
	}

	dashboard := e.get("/?year=" + itoa64(e.yearID64))
	if !strings.Contains(dashboard, "Stammdaten-Hinweis") || !strings.Contains(dashboard, "/data-quality?year=") {
		t.Fatalf("dashboard work queue omitted data quality")
	}
	page := e.get("/data-quality?year=" + itoa64(e.yearID64))
	for _, want := range []string{"Datenqualität", "E-Mail fehlt", "IBAN fehlt", "Betriebs-IBAN fehlt"} {
		if !strings.Contains(page, want) {
			t.Errorf("data quality page missing %q", want)
		}
	}
	for _, unwanted := range []string{"Selbstkosten fehlen", "Steuermodus unvollständig", "Steuerhinweis fehlt"} {
		if strings.Contains(page, unwanted) {
			t.Errorf("data quality page contains inapplicable finding %q", unwanted)
		}
	}
	if !strings.Contains(page, "/neighbors/") {
		t.Errorf("neighbor findings lack direct correction links")
	}

	company.TaxNote = "  "
	if err := e.st.UpdateCompany(e.ctx, company); err != nil {
		t.Fatal(err)
	}
	page = e.get("/data-quality?year=" + itoa64(e.yearID64))
	if !strings.Contains(page, "Steuerhinweis fehlt") || !strings.Contains(page, "Kleinunternehmerregelung benötigt den Hinweistext am Beleg.") {
		t.Error("Kleinunternehmer without an invoice notice was not reported")
	}
}
