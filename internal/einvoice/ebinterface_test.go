package einvoice

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

func testInvoice() models.Invoice {
	return models.Invoice{
		ID: 7, Number: "R2026-007", Kind: "invoice", Status: "issued",
		IssuedOn: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), PaymentReference: "RF001234",
		Content: &models.InvoiceContent{
			Net: decimal.NewFromInt(100), VATRate: decimal.NewFromInt(20),
			VATAmount: decimal.NewFromInt(20), Gross: decimal.NewFromInt(120), ShowVAT: true,
			TaxMode: "regel", TaxNote: "20 % Umsatzsteuer", PaymentTermDays: 14,
			Issuer:    models.InvoiceParty{Name: "Hof Bergmann", TaxID: "ATU12345678", IBAN: "AT611904300234573201", Street: "Feldweg 3", ZIP: "4780", Town: "Schärding", CountryCode: "AT"},
			Recipient: models.InvoiceParty{Name: "Josef Öllinger", TaxID: "ATU87654321", Street: "Dorfstraße 5", ZIP: "4780", Town: "Schärding", CountryCode: "AT", OrderID: "AUF-2026-7"},
			Lines:     []models.InvoiceLine{{Label: "Mäharbeit", Unit: "h", Quantity: decimal.NewFromInt(2), UnitPrice: decimal.NewFromInt(50), Cost: decimal.NewFromInt(100)}},
		},
	}
}

func TestRenderEbInterface(t *testing.T) {
	b, err := Render(testInvoice())
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		XMLName xml.Name
		Number  string `xml:"InvoiceNumber"`
	}
	if err := xml.Unmarshal(b, &root); err != nil {
		t.Fatalf("generated XML is not well formed: %v", err)
	}
	if root.XMLName.Space != Namespace || root.XMLName.Local != "Invoice" || root.Number != "R2026-007" {
		t.Fatalf("unexpected root: %#v", root)
	}
	for _, want := range []string{"AUF-2026-7", "Mäharbeit", "AT611904300234573201", "2026-10-13"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("generated XML missing %q", want)
		}
	}
	if path := os.Getenv("EBINTERFACE_TEST_OUTPUT"); path != "" {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMissingFieldsRejectsLegacyAndIncomplete(t *testing.T) {
	iv := testInvoice()
	iv.Content.Recipient.OrderID = ""
	missing := MissingFields(iv)
	if !strings.Contains(strings.Join(missing, ","), "Auftragsreferenz") {
		t.Fatalf("missing fields = %v", missing)
	}
	iv.Content = nil
	if _, err := Render(iv); err == nil {
		t.Fatal("legacy invoice without snapshot was exported")
	}
}
