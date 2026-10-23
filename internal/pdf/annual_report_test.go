package pdf

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/testutil/pdftext"
)

// TestRenderAnnualReport prints every section with German amounts, marks a
// neighbor's credit and a canceled invoice, and keeps a long account list
// readable across pages.
func TestRenderAnnualReport(t *testing.T) {
	t.Parallel()
	d := decimal.RequireFromString
	report := AnnualReport{
		IssuerName: "Übungshof", IssuerAddress: "Musterweg 1\n4710 Beispieldorf", Year: 2026, Completed: true,
		Generated: time.Date(2027, 1, 15, 9, 30, 0, 0, time.UTC),
		Bookings:  d("12345.60"), Hours: d("321.5"), Ledger: d("-400"), Paid: d("10000"), Open: d("1945.60"), Credit: d("50"),
		Tasks: []AnnualTask{{Label: "Mähen", Count: 12, Hours: d("40"), Cost: d("1840")}, {Count: 1, Hours: d("1"), Cost: d("46")}},
		Machines: []AnnualMachine{{Name: "Scheibenmähwerk", Hours: d("40"), Revenue: d("720"), Margin: d("310.50"), HasMargin: true},
			{Name: "Schwader", Hours: d("12"), Revenue: d("120")}},
		MarginTotal: d("310.50"), HasMargin: true,
		Invoices: []AnnualInvoice{
			{Number: "2026-001", Kind: "Rechnung", Neighbor: "Huber", Status: "storniert", IssuedOn: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Net: d("100"), VAT: d("13"), Gross: d("113")},
			{Number: "2026-002", Kind: "Storno", Neighbor: "Huber", IssuedOn: time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC), Net: d("-100"), VAT: d("-13"), Gross: d("-113")},
		},
		InvoiceNet: d("0"), InvoiceVAT: d("0"), InvoiceGross: d("0"),
	}
	for i := range 70 {
		report.Neighbors = append(report.Neighbors, AnnualNeighbor{Name: fmt.Sprintf("Nachbar %02d", i), Entries: 3,
			Hours: d("4.5"), Owed: d("207"), Paid: d("200"), Remaining: d("7")})
	}
	report.Neighbors = append(report.Neighbors, AnnualNeighbor{Name: "Guthaben-Nachbar", Owed: d("0"), Paid: d("50"), Remaining: d("-50")})

	data, err := RenderAnnualReport(report)
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
	for _, want := range []string{"JAHRESBERICHT 2026", "Jahr abgeschlossen", "12.345,60 €", "321,5 h", "Guthaben der Nachbarn",
		"Konten je Nachbar", "Nachbar 69", "Guthaben 50,00 €", "Leistungen nach Tätigkeit", "Mähen", "Ohne Tätigkeit",
		"Maschinenauslastung", "Scheibenmähwerk", "310,50 €", "Rechnungsjournal", "2026-001 · Rechnung (storniert)",
		"2026-002 · Storno", "-113,00 €", "Umsatzwirksam gesamt"} {
		if !strings.Contains(text, want) {
			t.Errorf("annual report lacks %q", want)
		}
	}
	if len(pages) < 2 {
		t.Errorf("71 accounts should span at least two pages, got %d", len(pages))
	}
}
