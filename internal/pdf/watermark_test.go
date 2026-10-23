package pdf

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/testutil/pdftext"
)

// TestWatermarkStampsEveryPage renders a multi-page invoice with and without the
// practice-instance stamp: with it, every page carries the text in the top band;
// without it, the document contains no stamp at all. Not parallel: the stamp is
// process-wide, and parallel tests resume only after this one has reset it.
func TestWatermarkStampsEveryPage(t *testing.T) {
	const mark = "ÜBUNGSINSTANZ · KEIN ECHTER BELEG"
	invoice := paymentTestInvoice()
	rows := make([]models.SettlementLine, 0, 120)
	for i := range 120 {
		rows = append(rows, models.SettlementLine{Date: invoice.IssuedOn, Kind: "payment",
			Label: fmt.Sprintf("Teilzahlung %03d", i), Amount: decimal.NewFromInt(1)})
	}
	status := InvoicePaymentStatus{AsOf: invoice.IssuedOn, Remaining: decimal.RequireFromString("157.20"), Lines: rows}
	stamped := func() map[int]bool {
		t.Helper()
		data, err := RenderInvoiceWithPaymentStatus(invoice, status)
		if err != nil {
			t.Fatal(err)
		}
		runs, err := pdftext.Extract(data)
		if err != nil {
			t.Fatal(err)
		}
		pages := map[int]bool{}
		for _, run := range runs {
			if _, seen := pages[run.Page]; !seen {
				pages[run.Page] = false
			}
			if run.Text == mark {
				if run.Y < 800 {
					t.Fatalf("stamp on page %d sits in the content area (y=%.1f)", run.Page, run.Y)
				}
				pages[run.Page] = true
			}
		}
		return pages
	}

	for page, has := range stamped() {
		if has {
			t.Fatalf("page %d stamped although no watermark is set", page)
		}
	}
	SetWatermark(mark)
	t.Cleanup(func() { SetWatermark("") })
	pages := stamped()
	if len(pages) < 3 {
		t.Fatalf("fixture should span at least 3 pages, got %d", len(pages))
	}
	for page, has := range pages {
		if !has {
			t.Fatalf("page %d lacks the practice-instance stamp", page)
		}
	}
}
