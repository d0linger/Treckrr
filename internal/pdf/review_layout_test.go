package pdf

import (
	"fmt"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/testutil/pdftext"
)

func TestAnnualReportTotalsStayAboveFooter(t *testing.T) {
	for n := 1; n <= 65; n++ {
		a := AnnualReport{Year: 2026, HasMargin: true}
		for i := range n {
			a.Machines = append(a.Machines, AnnualMachine{Name: fmt.Sprintf("Machine %d", i)})
			a.Invoices = append(a.Invoices, AnnualInvoice{Number: fmt.Sprintf("INV-%d", i)})
		}
		data, err := RenderAnnualReport(a)
		if err != nil {
			t.Fatal(err)
		}
		runs, err := pdftext.Extract(data)
		if err != nil {
			t.Fatal(err)
		}
		lastInvoicePage, totalPage := 0, 0
		lastMachinePage, marginPage := 0, 0
		for _, r := range runs {
			if r.Text == fmt.Sprintf("Machine %d", n-1) {
				lastMachinePage = r.Page
			}
			if strings.HasPrefix(r.Text, "Deckungsbeitrag gesamt") {
				marginPage = r.Page
			}
			if strings.HasPrefix(r.Text, fmt.Sprintf("INV-%d ·", n-1)) {
				lastInvoicePage = r.Page
			}
			if r.Text == "Umsatzwirksam gesamt" {
				totalPage = r.Page
			}
			if (strings.HasPrefix(r.Text, "Deckungsbeitrag gesamt") || r.Text == "Umsatzwirksam gesamt") && r.Y < 60 {
				t.Fatalf("%d rows: total %q overlaps footer area at Y %.2f", n, r.Text, r.Y)
			}
		}
		if lastInvoicePage == 0 || lastInvoicePage != totalPage {
			t.Fatalf("%d rows: total orphaned from final invoice row", n)
		}
		if lastMachinePage == 0 || lastMachinePage != marginPage {
			t.Fatalf("%d rows: margin orphaned from final machine row", n)
		}
	}
}

func TestPriceSheetEmptyRigsAndLongTextStayReadable(t *testing.T) {
	p := PriceSheet{Rigs: []PriceSheetRig{{Name: "Empty A"}, {Name: "Empty B"},
		{Name: "Long rig", Parts: strings.Repeat("Synthetic component with a long name + ", 200)}}, TaxNote: strings.Repeat("WWWWWWWWW ", 50)}
	data, err := RenderPriceSheet(p)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := pdftext.Extract(data)
	if err != nil {
		t.Fatal(err)
	}
	var first, second pdftext.Run
	noteLines := 0
	for _, r := range runs {
		if r.Text == "Empty A" {
			first = r
		}
		if r.Text == "Empty B" {
			second = r
		}
		if strings.Contains(r.Text, "WWWWW") || strings.Contains(r.Text, "Synthetic") {
			if r.Y < 60 {
				t.Fatalf("content overlaps footer: %+v", r)
			}
		}
		noteLines += strings.Count(r.Text, "W")
	}
	if first.Page == 0 || second.Page == 0 || (first.Page == second.Page && first.Y-second.Y < 12) {
		t.Fatalf("empty rig rows overlap: %+v %+v", first, second)
	}
	if noteLines != 450 {
		t.Fatalf("tax note truncated: %d letters", noteLines)
	}
}

func TestPriceSheetTaxNoteAtPageBoundaries(t *testing.T) {
	for n := 1; n <= 50; n++ {
		p := PriceSheet{TaxNote: strings.Repeat("WWWWWWWWW ", 50)}
		for i := range n {
			p.Machines = append(p.Machines, PriceSheetMachine{Name: fmt.Sprintf("Machine %d", i)})
		}
		data, err := RenderPriceSheet(p)
		if err != nil {
			t.Fatal(err)
		}
		runs, err := pdftext.Extract(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if strings.Contains(r.Text, "WWWWW") && r.Y < 60 {
				t.Fatalf("%d machines: tax note in footer at %.2f", n, r.Y)
			}
		}
	}
}

func TestReminderRejectsInconsistentSettlementExplanation(t *testing.T) {
	m := MahnungData{Open: decimal.NewFromInt(80), Paid: decimal.NewFromInt(20),
		SettlementLines: []models.SettlementLine{{Kind: "payment", Label: "Synthetic payment", Amount: decimal.NewFromInt(30)}}}
	if _, err := RenderMahnung(m); err == nil {
		t.Fatal("contradictory settlement explanation rendered")
	}
	m.SettlementLines[0].Amount = m.Paid
	if _, err := RenderMahnung(m); err != nil {
		t.Fatal(err)
	}
}
