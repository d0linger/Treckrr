package pdf

import (
	"bytes"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// A4 layout constants (points).
const (
	pageW    = 595.28
	marginL  = 42.0
	marginR  = 42.0
	contentW = pageW - marginL - marginR
)

// money formats a decimal in German notation with a trailing € (e.g. 1.234,56 €).
func money(d decimal.Decimal) string {
	s := d.StringFixed(2) // "1234.56" / "-50.00"
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	intPart, frac := s, "00"
	if i := strings.IndexByte(s, '.'); i >= 0 {
		intPart, frac = s[:i], s[i+1:]
	}
	var b strings.Builder
	n := len(intPart)
	for i, c := range intPart {
		if i > 0 && (n-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	out := b.String() + "," + frac + " €"
	if neg {
		out = "-" + out
	}
	return out
}

// RenderInvoice builds the festgeschriebene Rechnung as a one-or-more-page A4 PDF
// from the frozen invoice snapshot — never from live data, so a re-issue is
// byte-stable. iv.Content must be non-nil.
func RenderInvoice(iv *models.Invoice) ([]byte, error) {
	c := iv.Content
	pdf, err := newDoc()
	if err != nil {
		return nil, err
	}
	pdf.AddPage()
	y := 48.0

	text := func(x, yy, size float64, bold bool, s string) {
		f := fontRegular
		if bold {
			f = fontBold
		}
		_ = pdf.SetFont(f, "", size)
		pdf.SetXY(x, yy)
		_ = pdf.Cell(nil, s)
	}
	// right-aligned text ending at x=right
	textR := func(right, yy, size float64, bold bool, s string) {
		f := fontRegular
		if bold {
			f = fontBold
		}
		_ = pdf.SetFont(f, "", size)
		w, _ := pdf.MeasureTextWidth(s)
		pdf.SetXY(right-w, yy)
		_ = pdf.Cell(nil, s)
	}
	block := func(x, yy, size float64, s string) float64 {
		for _, line := range strings.Split(s, "\n") {
			if strings.TrimSpace(line) == "" {
				yy += size * 0.6
				continue
			}
			text(x, yy, size, false, strings.TrimSpace(line))
			yy += size * 1.25
		}
		return yy
	}

	// Header: issuer (sender) block, left.
	text(marginL, y, 15, true, firstNonEmpty(c.Issuer.Name, "Rechnung"))
	gaccent(pdf, marginL, y+18)
	y += 24
	yIssuer := block(marginL, y, 9.5, c.Issuer.Address)
	if strings.TrimSpace(c.Issuer.TaxID) != "" {
		text(marginL, yIssuer, 9.5, false, "UID/Steuernr.: "+c.Issuer.TaxID)
		yIssuer += 12
	}

	// Invoice meta, right.
	right := pageW - marginR
	textR(right, y, 18, true, "RECHNUNG")
	textR(right, y+22, 10, false, "Nr. "+iv.Number)
	textR(right, y+35, 10, false, "Datum: "+iv.IssuedOn.Format("02.01.2006"))
	if !c.ServiceFrom.IsZero() {
		period := c.ServiceFrom.Format("02.01.2006")
		if !c.ServiceTo.IsZero() && !c.ServiceTo.Equal(c.ServiceFrom) {
			period += "–" + c.ServiceTo.Format("02.01.2006")
		}
		textR(right, y+48, 10, false, "Leistungszeitraum: "+period)
	}

	// Recipient block.
	y = maxf(yIssuer, y+62) + 16
	text(marginL, y, 9, false, "An")
	y += 13
	y = block(marginL, y, 10.5, c.Recipient.Name+"\n"+c.Recipient.Address)
	if strings.TrimSpace(c.Recipient.TaxID) != "" {
		text(marginL, y, 9.5, false, "UID: "+c.Recipient.TaxID)
		y += 12
	}
	y += 14

	// Line-item table header.
	colDate := marginL
	colQty := marginL + 300
	colPrice := marginL + 370
	colCost := pageW - marginR
	pdf.SetLineWidth(0.6)
	pdf.SetStrokeColor(120, 120, 120)
	text(colDate, y, 9, true, "Datum · Leistung")
	textR(colQty+18, y, 9, true, "Menge")
	textR(colPrice+18, y, 9, true, "Einzel")
	textR(colCost, y, 9, true, "Betrag")
	y += 13
	pdf.Line(marginL, y, pageW-marginR, y)
	y += 8

	for _, l := range c.Lines {
		if y > 760 { // new page
			pdf.AddPage()
			y = 56
		}
		label := l.Label
		if label == "" {
			label = "Leistung"
		}
		unit := l.Unit
		if unit == "" || unit == "h" {
			unit = "h"
		}
		text(colDate, y, 9.5, false, l.Date.Format("02.01.2006")+" · "+label)
		textR(colQty+18, y, 9.5, false, trimZeros(l.Quantity)+" "+unit)
		textR(colPrice+18, y, 9.5, false, money(l.UnitPrice))
		textR(colCost, y, 9.5, false, money(l.Cost))
		y += 14
	}

	// Totals.
	y += 4
	pdf.Line(colQty, y, pageW-marginR, y)
	y += 10
	if c.ShowVAT {
		textR(colPrice+18, y, 9.5, false, "Netto")
		textR(colCost, y, 9.5, false, money(c.Net))
		y += 14
		textR(colPrice+18, y, 9.5, false, "USt "+trimZeros(c.VATRate)+" %")
		textR(colCost, y, 9.5, false, money(c.VATAmount))
		y += 14
	}
	textR(colPrice+18, y, 11, true, "Gesamt")
	textR(colCost, y, 11, true, money(c.Gross))
	y += 22

	if strings.TrimSpace(c.TaxNote) != "" {
		y = block(marginL, y, 9, c.TaxNote) + 6
	}
	// Payment info.
	if strings.TrimSpace(c.Issuer.IBAN) != "" {
		text(marginL, y, 9.5, true, "Zahlung")
		y += 13
		line := "IBAN " + c.Issuer.IBAN
		if strings.TrimSpace(iv.PaymentReference) != "" {
			line += " · Verwendungszweck: " + iv.PaymentReference
		}
		text(marginL, y, 9.5, false, line)
		y += 14
		text(marginL, y, 9.5, false, "Betrag: "+money(c.Gross))
	}

	gfooter(pdf, c.Issuer.Name, c.Issuer.Address, c.Issuer.TaxID, c.Issuer.IBAN)
	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func maxf(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func trimZeros(d decimal.Decimal) string {
	s := d.String()
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return strings.ReplaceAll(s, ".", ",")
}
