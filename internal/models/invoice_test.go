package models

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// complete is a §11-valid Kleinunternehmer content (no VAT) used as the baseline
// from which each case removes exactly one requirement.
func complete() InvoiceContent {
	return InvoiceContent{
		Net: dec("218.00"), Gross: dec("218.00"), ShowVAT: false, TaxMode: "kleinunternehmer",
		Issuer:    InvoiceParty{Name: "Hof Bergmann", Address: "Feldweg 3"},
		Recipient: InvoiceParty{Name: "Florian", Address: "Dorfstraße 1"},
		Lines:     []InvoiceLine{{Label: "Mähen", Cost: dec("218.00")}},
	}
}

func TestMissingMandatory(t *testing.T) {
	if m := complete().MissingMandatory(); len(m) != 0 {
		t.Fatalf("complete content should be issuable, got: %v", m)
	}

	cases := []struct {
		name   string
		mutate func(*InvoiceContent)
		want   string // substring the flagged item must contain
	}{
		{"no issuer name", func(c *InvoiceContent) { c.Issuer.Name = "" }, "Absender-Name"},
		{"blank issuer address", func(c *InvoiceContent) { c.Issuer.Address = "  " }, "Absender-Adresse"},
		{"no recipient name", func(c *InvoiceContent) { c.Recipient.Name = "" }, "Empfänger-Name"},
		{"no recipient address", func(c *InvoiceContent) { c.Recipient.Address = "" }, "Empfänger-Adresse"},
		{"no lines", func(c *InvoiceContent) { c.Lines = nil }, "Leistungszeitraum"},
		{"regel without rate", func(c *InvoiceContent) { c.TaxMode = "regel"; c.ShowVAT = false }, "USt-Ausweis"},
		{"pauschal without rate", func(c *InvoiceContent) { c.TaxMode = "pauschal"; c.ShowVAT = false }, "USt-Ausweis"},
		{"over 10k without UID", func(c *InvoiceContent) { c.Gross = dec("10000.01"); c.Recipient.TaxID = "" }, "Empfänger-UID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := complete()
			tc.mutate(&c)
			m := c.MissingMandatory()
			found := false
			for _, s := range m {
				if strings.Contains(s, tc.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("expected a missing item containing %q, got: %v", tc.want, m)
			}
		})
	}
}

// A VAT invoice with a positive rate (ShowVAT true) is complete; Kleinunternehmer
// with no VAT is complete; and a large gross with a UID present is complete.
func TestMissingMandatoryValidCombinations(t *testing.T) {
	regel := complete()
	regel.TaxMode = "regel"
	regel.ShowVAT = true
	regel.VATRate = dec("13")
	regel.VATAmount = dec("28.34")
	regel.Gross = dec("246.34")
	if m := regel.MissingMandatory(); len(m) != 0 {
		t.Fatalf("valid regel invoice flagged: %v", m)
	}

	big := complete()
	big.Gross = dec("15000.00")
	big.Recipient.TaxID = "ATU99999"
	if m := big.MissingMandatory(); len(m) != 0 {
		t.Fatalf("valid >10k invoice with UID flagged: %v", m)
	}

	// Exactly at the threshold does not require a UID (§11: "über 10.000 €").
	at := complete()
	at.Gross = dec("10000.00")
	at.Recipient.TaxID = ""
	if m := at.MissingMandatory(); len(m) != 0 {
		t.Fatalf("gross exactly 10000 should not require UID: %v", m)
	}
}
