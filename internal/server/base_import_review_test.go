package server

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestRateExportImportPreservesNamesAndCase(t *testing.T) {
	names := []string{"=SUM(A1)", "'literal", "'=literal", "  @name", "Mähwerk", "mähwerk"}
	var loads []models.LoadLevel
	for _, name := range names {
		loads = append(loads, models.LoadLevel{Name: name, CostPerPS: decimal.RequireFromString("0.3627")})
	}
	for _, spreadsheet := range []bool{false, true} {
		raw, err := rateListCSV(loads, nil, nil, spreadsheet)
		if err != nil {
			t.Fatal(err)
		}
		rows, msg := parseRateList(raw)
		if msg != "" || len(rows) != len(names) {
			t.Fatalf("round trip: %q, %d rows", msg, len(rows))
		}
		for i, row := range rows {
			// Unmarked, hand-edited imports intentionally trim surrounding spaces.
			want := names[i]
			if !spreadsheet {
				want = strings.TrimSpace(want)
			}
			if row.Name != want || row.Err != "" || !row.CostPerPS.Equal(loads[i].CostPerPS) {
				t.Errorf("row %d: %+v, want %q", i, row, want)
			}
		}
		if spreadsheet {
			p := diffRateList(rows, loads, nil, nil)
			if p.Same != len(names) || p.Errors != 0 || len(p.Removed) != 0 {
				t.Fatalf("identity changed: %+v", p)
			}
		}
	}
	rows, msg := parseRateList([]byte("Art;Name;Kosten je PS·h\nBelastungsstufe;'=literal;0,1\n"))
	if msg != "" || rows[0].Name != "'=literal" {
		t.Fatal("unmarked literal apostrophe was stripped")
	}
}

func TestRateImportReportsPhysicalLines(t *testing.T) {
	rows, msg := parseRateList([]byte("\nArt;Name;Kosten je PS·h\n\nBelastungsstufe;\"zwei\nZeilen\";0,1\n\nBelastungsstufe;\"zwei\nZeilen\";0,2\n"))
	if msg != "" || len(rows) != 2 || rows[0].Line != 4 || rows[1].Line != 7 || !strings.Contains(rows[1].Err, "Zeile 4") {
		t.Fatalf("physical lines: %+v, %q", rows, msg)
	}
	_, msg = parseRateList([]byte("Art;Name\n\nBelastungsstufe;\"unterminated"))
	if !strings.Contains(msg, "Zeile 3") {
		t.Fatalf("parse error location: %q", msg)
	}
}

func TestZeroUpliftPreservesExactRates(t *testing.T) {
	for _, step := range upliftSteps {
		rate := decimal.RequireFromString("6.2727")
		if got := upliftRate(rate, decimal.Zero, decimal.RequireFromString(step)); !got.Equal(rate) {
			t.Fatalf("0%% changed %s to %s with step %s", rate, got, step)
		}
	}
}
