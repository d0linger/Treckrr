package pdf

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/testutil/pdftext"
)

// TestRenderPriceSheet prints every section with German amounts and keeps a
// long tractor list readable across pages.
func TestRenderPriceSheet(t *testing.T) {
	t.Parallel()
	d := decimal.RequireFromString
	sheet := PriceSheet{
		IssuerName: "Übungshof", IssuerAddress: "Musterweg 1\n4710 Beispieldorf", BaseName: "Grundlage 2026", ValidFrom: 2026,
		Today:      time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC),
		Loads:      []PriceSheetLoad{{Name: "leicht", CostPerPS: d("0.33")}, {Name: "schwer", CostPerPS: d("0.38")}},
		Machines:   []PriceSheetMachine{{Name: "Scheibenmähwerk", Category: "Grünland", Width: d("3"), CostPerAB: d("6"), Rate: d("18")}},
		Rigs:       []PriceSheetRig{{Name: "Mähen 3 m", Parts: "T1 Allrad 120 · mittel · Scheibenmähwerk", Rate: d("61.20")}},
		TravelFlat: d("12.5"), TaxNote: "Alle Sätze netto, zuzüglich 13 % Umsatzsteuer.",
	}
	for i := range 60 {
		sheet.Tractors = append(sheet.Tractors, PriceSheetTractor{Label: fmt.Sprintf("T%02d Allrad", i), PS: d("120"),
			Rates: []decimal.Decimal{d("39.60"), d("45.60")}})
	}
	data, err := RenderPriceSheet(sheet)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := pdftext.Extract(data)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	pages := map[int]bool{}
	for _, run := range runs {
		texts = append(texts, run.Text)
		pages[run.Page] = true
	}
	text := strings.Join(texts, "\n")
	for _, want := range []string{"PREISBLATT", "gültig ab 2026", "Traktoren je Belastungsstufe", "T59 Allrad", "45,60 €",
		"Scheibenmähwerk", "18,00 €", "Mähen 3 m", "61,20 €", "Pauschale je Anfahrt: 12,50 €", "zuzüglich 13 % Umsatzsteuer"} {
		if !strings.Contains(text, want) {
			t.Errorf("price sheet lacks %q", want)
		}
	}
	if len(pages) < 2 {
		t.Errorf("60 tractors should span at least two pages, got %d", len(pages))
	}
}
