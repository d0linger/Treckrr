// Package einvoice renders structured invoices from immutable Treckrr invoice
// snapshots. It deliberately never reconstructs historical party data.
package einvoice

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// Namespace is the official ebInterface 6.1 XML namespace.
const Namespace = "http://www.ebinterface.at/schema/6p1/"

// MissingFields lists data required for Treckrr's ebInterface 6.1 profile.
func MissingFields(iv models.Invoice) []string {
	var missing []string
	if iv.Kind != "invoice" || iv.Status != "issued" {
		missing = append(missing, "ausgestellte Rechnung")
	}
	if iv.Content == nil {
		return append(missing, "festgeschriebener Rechnungssnapshot")
	}
	c := iv.Content
	for _, item := range []struct{ label, value string }{
		{"Absender-UID", c.Issuer.TaxID}, {"Empfänger-UID", c.Recipient.TaxID},
		{"Absender-Straße", c.Issuer.Street}, {"Absender-PLZ", c.Issuer.ZIP},
		{"Absender-Ort", c.Issuer.Town}, {"Absender-Ländercode", c.Issuer.CountryCode},
		{"Empfänger-Straße", c.Recipient.Street}, {"Empfänger-PLZ", c.Recipient.ZIP},
		{"Empfänger-Ort", c.Recipient.Town}, {"Empfänger-Ländercode", c.Recipient.CountryCode},
		{"Auftragsreferenz", c.Recipient.OrderID},
	} {
		if strings.TrimSpace(item.value) == "" {
			missing = append(missing, item.label)
		}
	}
	if len(c.Lines) == 0 {
		missing = append(missing, "Rechnungspositionen")
	}
	for _, line := range c.Lines {
		if !line.Quantity.IsPositive() {
			missing = append(missing, "positive Menge je Rechnungsposition")
			break
		}
	}
	return missing
}

type country struct {
	Code string `xml:"CountryCode,attr"`
	Name string `xml:",chardata"`
}

type address struct {
	Name    string  `xml:"Name"`
	Street  string  `xml:"Street"`
	Town    string  `xml:"Town"`
	ZIP     string  `xml:"ZIP"`
	Country country `xml:"Country"`
}

type orderReference struct {
	OrderID string `xml:"OrderID"`
}

type party struct {
	VATID          string          `xml:"VATIdentificationNumber"`
	OrderReference *orderReference `xml:"OrderReference,omitempty"`
	Address        address         `xml:"Address"`
}

type amount struct {
	Value string `xml:",chardata"`
}

type percent struct {
	Category string `xml:"TaxCategoryCode,attr"`
	Value    string `xml:",chardata"`
}

type taxItem struct {
	TaxableAmount amount  `xml:"TaxableAmount"`
	TaxPercent    percent `xml:"TaxPercent"`
	TaxAmount     amount  `xml:"TaxAmount"`
	Comment       string  `xml:"Comment,omitempty"`
}

type quantity struct {
	Unit  string `xml:"Unit,attr"`
	Value string `xml:",chardata"`
}

type lineItem struct {
	PositionNumber int      `xml:"PositionNumber"`
	Description    string   `xml:"Description"`
	Quantity       quantity `xml:"Quantity"`
	UnitPrice      amount   `xml:"UnitPrice"`
	TaxItem        taxItem  `xml:"TaxItem"`
	LineItemAmount amount   `xml:"LineItemAmount"`
}

type itemList struct {
	Items []lineItem `xml:"ListLineItem"`
}
type details struct {
	Items itemList `xml:"ItemList"`
}
type tax struct {
	Items []taxItem `xml:"TaxItem"`
}

type beneficiaryAccount struct {
	IBAN  string `xml:"IBAN"`
	Owner string `xml:"BankAccountOwner"`
}

type bankTransaction struct {
	Account   beneficiaryAccount `xml:"BeneficiaryAccount"`
	Reference string             `xml:"PaymentReference,omitempty"`
}

type paymentMethod struct {
	Comment string           `xml:"Comment"`
	Bank    *bankTransaction `xml:"UniversalBankTransaction,omitempty"`
}

type paymentConditions struct {
	DueDate string `xml:"DueDate"`
}

type invoiceXML struct {
	XMLName           xml.Name          `xml:"Invoice"`
	XMLNS             string            `xml:"xmlns,attr"`
	Generating        string            `xml:"GeneratingSystem,attr"`
	DocumentType      string            `xml:"DocumentType,attr"`
	Currency          string            `xml:"InvoiceCurrency,attr"`
	Title             string            `xml:"DocumentTitle,attr"`
	Language          string            `xml:"Language,attr"`
	Number            string            `xml:"InvoiceNumber"`
	Date              string            `xml:"InvoiceDate"`
	Biller            party             `xml:"Biller"`
	Recipient         party             `xml:"InvoiceRecipient"`
	Details           details           `xml:"Details"`
	Tax               tax               `xml:"Tax"`
	TotalGross        amount            `xml:"TotalGrossAmount"`
	Payable           amount            `xml:"PayableAmount"`
	PaymentMethod     paymentMethod     `xml:"PaymentMethod"`
	PaymentConditions paymentConditions `xml:"PaymentConditions"`
}

func countryName(code string) string {
	switch code {
	case "AT":
		return "Österreich"
	case "DE":
		return "Deutschland"
	case "CH":
		return "Schweiz"
	case "IT":
		return "Italien"
	default:
		return code
	}
}

func xmlParty(p models.InvoiceParty, recipient bool) party {
	x := party{VATID: strings.TrimSpace(p.TaxID), Address: address{
		Name: p.Name, Street: p.Street, Town: p.Town, ZIP: p.ZIP,
		Country: country{Code: p.CountryCode, Name: countryName(p.CountryCode)},
	}}
	if recipient {
		x.OrderReference = &orderReference{OrderID: p.OrderID}
	}
	return x
}

func unitCode(unit string) string {
	if unit == "h" || unit == "" {
		return "HUR"
	}
	return "C62"
}

// Render creates an ebInterface 6.1 XML document from a frozen invoice.
func Render(iv models.Invoice) ([]byte, error) {
	if missing := MissingFields(iv); len(missing) > 0 {
		return nil, fmt.Errorf("e-invoice incomplete: %s", strings.Join(missing, ", "))
	}
	c := iv.Content
	category := "E"
	if c.ShowVAT && c.VATRate.IsPositive() {
		category = "S"
	}
	items := make([]lineItem, 0, len(c.Lines))
	for i, line := range c.Lines {
		lineTax := decimal.Zero
		if c.ShowVAT {
			lineTax = line.Cost.Mul(c.VATRate).Div(decimal.NewFromInt(100)).Round(2)
		}
		items = append(items, lineItem{
			PositionNumber: i + 1, Description: line.Label,
			Quantity:  quantity{Unit: unitCode(line.Unit), Value: line.Quantity.String()},
			UnitPrice: amount{Value: line.UnitPrice.StringFixed(4)},
			TaxItem: taxItem{TaxableAmount: amount{Value: line.Cost.StringFixed(2)},
				TaxPercent: percent{Category: category, Value: c.VATRate.String()},
				TaxAmount:  amount{Value: lineTax.StringFixed(2)}, Comment: c.TaxNote},
			LineItemAmount: amount{Value: line.Cost.StringFixed(2)},
		})
	}
	doc := invoiceXML{
		XMLNS: Namespace, Generating: "Treckrr", DocumentType: "Invoice", Currency: "EUR",
		Title: "Rechnung", Language: "de", Number: iv.Number, Date: iv.IssuedOn.Format("2006-01-02"),
		Biller: xmlParty(c.Issuer, false), Recipient: xmlParty(c.Recipient, true),
		Details: details{Items: itemList{Items: items}},
		Tax: tax{Items: []taxItem{{TaxableAmount: amount{Value: c.Net.StringFixed(2)},
			TaxPercent: percent{Category: category, Value: c.VATRate.String()},
			TaxAmount:  amount{Value: c.VATAmount.StringFixed(2)}, Comment: c.TaxNote}}},
		TotalGross: amount{Value: c.Gross.StringFixed(2)}, Payable: amount{Value: c.Gross.StringFixed(2)},
		PaymentMethod:     paymentMethod{Comment: "Zahlbar per Überweisung."},
		PaymentConditions: paymentConditions{DueDate: iv.IssuedOn.AddDate(0, 0, c.PaymentTermDays).Format("2006-01-02")},
	}
	if strings.TrimSpace(c.Issuer.IBAN) != "" {
		doc.PaymentMethod.Bank = &bankTransaction{
			Account:   beneficiaryAccount{IBAN: strings.ReplaceAll(c.Issuer.IBAN, " ", ""), Owner: c.Issuer.Name},
			Reference: iv.PaymentReference,
		}
	}
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	if !bytes.Contains(buf.Bytes(), []byte(`xmlns="`+Namespace+`"`)) {
		return nil, errors.New("e-invoice namespace missing")
	}
	return buf.Bytes(), nil
}
