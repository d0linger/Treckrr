package server

import (
	"strings"
	"testing"
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
	for _, code := range []string{"neighbor_email", "neighbor_iban", "machine_self_cost"} {
		if !codes[code] {
			t.Errorf("fixture finding %q missing: %+v", code, report.Issues)
		}
	}
	if codes["neighbor_address"] || codes["machine_rate"] {
		t.Errorf("complete fixture fields were reported missing: %+v", report.Issues)
	}

	dashboard := e.get("/?year=" + itoa64(e.yearID64))
	if !strings.Contains(dashboard, "Stammdaten-Hinweis") || !strings.Contains(dashboard, "/data-quality?year=") {
		t.Fatalf("dashboard work queue omitted data quality")
	}
	page := e.get("/data-quality?year=" + itoa64(e.yearID64))
	for _, want := range []string{"Datenqualität", "E-Mail fehlt", "IBAN fehlt", "Selbstkosten fehlen", "Betriebs-IBAN fehlt"} {
		if !strings.Contains(page, want) {
			t.Errorf("data quality page missing %q", want)
		}
	}
	if !strings.Contains(page, "#machine-") || !strings.Contains(page, "/neighbors/") {
		t.Errorf("findings lack direct correction links")
	}
}
