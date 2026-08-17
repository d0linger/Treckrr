package pdf

import (
	"bytes"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func TestRenderInvoice(t *testing.T) {
	iv := &models.Invoice{
		Number: "2026-007", IssuedOn: time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC),
		PaymentReference: "RG-2026-007",
		Content: &models.InvoiceContent{
			Net: decimal.RequireFromString("1000.00"), VATRate: decimal.RequireFromString("13"),
			VATAmount: decimal.RequireFromString("130.00"), Gross: decimal.RequireFromString("1130.00"),
			ShowVAT: true, TaxNote: "Umsatzsteuer 13 % (pauschal).",
			ServiceFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			ServiceTo:   time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
			Issuer:      models.InvoiceParty{Name: "Maschinenring Müller", Address: "Feldweg 3\n4780 Schärding", TaxID: "ATU12345678", IBAN: "AT61 1904 3002 3457 3201"},
			Recipient:   models.InvoiceParty{Name: "Josef Öllinger", Address: "Dorfstraße 5\n4780 Schärding"},
			Lines: []models.InvoiceLine{
				{Date: time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC), Label: "Mähen", Unit: "h", Quantity: decimal.RequireFromString("12.5"), UnitPrice: decimal.RequireFromString("46.00"), Cost: decimal.RequireFromString("575.00")},
				{Date: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC), Label: "Ballenpressen", Unit: "Ballen", Quantity: decimal.RequireFromString("120"), UnitPrice: decimal.RequireFromString("3.20"), Cost: decimal.RequireFromString("384.00")},
			},
		},
	}
	b, err := RenderInvoice(iv)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) || len(b) < 2000 {
		t.Fatalf("invalid/too-small invoice PDF (%d bytes)", len(b))
	}
}

func TestRenderMahnung(t *testing.T) {
	b, err := RenderMahnung(MahnungData{
		IssuerName: "MR Müller", IssuerAddress: "Feldweg 3\n4780 Schärding", IssuerIBAN: "AT61 1904 3002 3457 3201",
		RecipientName: "Josef Öllinger", RecipientAddr: "Dorfstraße 5\n4780 Schärding",
		Title: "1. Mahnung", Intro: "Trotz unserer Zahlungserinnerung ist der folgende Betrag noch offen. Wir bitten um Überweisung binnen 14 Tagen.",
		InvoiceNo: "2026-003", IssuedOn: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), DueOn: time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC),
		Open: decimal.RequireFromString("575.00"), Paid: decimal.RequireFromString("0"), Today: time.Now(),
	})
	if err != nil || !bytes.HasPrefix(b, []byte("%PDF-")) || len(b) < 2000 {
		t.Fatalf("mahnung PDF invalid: err=%v size=%d", err, len(b))
	}
}

func TestRenderStatement(t *testing.T) {
	b, err := RenderStatement(StatementData{
		IssuerName: "MR Müller", RecipientName: "Josef Öllinger", RecipientAddr: "Dorfstraße 5",
		Rows: []StatementYear{
			{Year: 2025, Cost: decimal.RequireFromString("1200.00"), Hours: decimal.RequireFromString("26"), Paid: true},
			{Year: 2026, Cost: decimal.RequireFromString("575.00"), Hours: decimal.RequireFromString("12.5"), Paid: false},
		},
		TotalCost: decimal.RequireFromString("1775.00"), TotalHours: decimal.RequireFromString("38.5"), Today: time.Now(),
	})
	if err != nil || !bytes.HasPrefix(b, []byte("%PDF-")) || len(b) < 2000 {
		t.Fatalf("statement PDF invalid: err=%v size=%d", err, len(b))
	}
}

func TestMoney(t *testing.T) {
	cases := map[string]string{"1234.56": "1.234,56 €", "0": "0,00 €", "-50": "-50,00 €", "1000000": "1.000.000,00 €"}
	for in, want := range cases {
		if got := money(decimal.RequireFromString(in)); got != want {
			t.Errorf("money(%s)=%q, want %q", in, got, want)
		}
	}
}

// TestSelfTest proves the embedded Go font renders German umlauts + € via gopdf
// and produces a structurally valid PDF (so we don't need to bundle a TTF).
func TestSelfTest(t *testing.T) {
	b, err := selfTest()
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatal("output is not a PDF")
	}
	if !bytes.Contains(b, []byte("EOF")) {
		t.Fatal("PDF has no EOF trailer")
	}
	if len(b) < 1500 {
		t.Fatalf("PDF suspiciously small: %d bytes", len(b))
	}
}
