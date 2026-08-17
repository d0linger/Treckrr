package pdf

import (
	"bytes"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// MahnungData is everything the reminder PDF renders (no live recomputation).
type MahnungData struct {
	IssuerName    string
	IssuerAddress string
	IssuerIBAN    string
	RecipientName string
	RecipientAddr string
	Title         string // "Zahlungserinnerung" / "1. Mahnung" …
	Intro         string
	InvoiceNo     string
	IssuedOn      time.Time
	DueOn         time.Time // zero = omit
	Open          decimal.Decimal
	Paid          decimal.Decimal
	Today         time.Time
}

// RenderMahnung builds an A4 reminder PDF.
func RenderMahnung(m MahnungData) ([]byte, error) {
	pdf, err := newDoc()
	if err != nil {
		return nil, err
	}
	pdf.AddPage()
	right := pageW - marginR
	y := 48.0

	gtext(pdf, marginL, y, 15, true, firstNonEmpty(m.IssuerName, "Mahnung"))
	gaccent(pdf, marginL, y+18)
	y += 24
	yIss := gblock(pdf, marginL, y, 9.5, m.IssuerAddress)

	gtextR(pdf, right, y, 18, true, strings.ToUpper(firstNonEmpty(m.Title, "Mahnung")))
	if !m.Today.IsZero() {
		gtextR(pdf, right, y+22, 10, false, "Datum: "+m.Today.Format("02.01.2006"))
	}

	// recipient
	y = maxf(yIss, y+40) + 16
	gtext(pdf, marginL, y, 9, false, "An")
	y += 13
	y = gblock(pdf, marginL, y, 10.5, m.RecipientName+"\n"+m.RecipientAddr) + 18

	// reference line
	ref := "Rechnung " + m.InvoiceNo
	if !m.IssuedOn.IsZero() {
		ref += " vom " + m.IssuedOn.Format("02.01.2006")
	}
	if !m.DueOn.IsZero() {
		ref += " · fällig war der " + m.DueOn.Format("02.01.2006")
	}
	gtext(pdf, marginL, y, 10, true, ref)
	y += 22

	// intro text (wraps at ~95 chars per line manually — gopdf has no auto-wrap).
	// Break to a new page before a line would run into the footer for a long intro.
	for _, line := range wrap(m.Intro, 95) {
		if y > 720 {
			pdf.AddPage()
			y = 56
		}
		gtext(pdf, marginL, y, 10.5, false, line)
		y += 16
	}
	y += 8

	// Keep the amount box + paid line + Zahlung/IBAN block together above the footer
	// (drawn at y=806): if a long intro pushed us near the bottom, this whole block
	// (~80pt) would otherwise overrun it, so start a fresh page first.
	if y > 700 {
		pdf.AddPage()
		y = 56
	}
	// amount box
	gtext(pdf, marginL, y, 11, true, "Offener Betrag")
	gtextR(pdf, right, y, 13, true, money(m.Open))
	y += 18
	if m.Paid.IsPositive() {
		gtext(pdf, marginL, y, 9.5, false, "Bereits bezahlt")
		gtextR(pdf, right, y, 9.5, false, money(m.Paid))
		y += 16
	}
	y += 10

	if strings.TrimSpace(m.IssuerIBAN) != "" && m.Open.IsPositive() {
		gtext(pdf, marginL, y, 9.5, true, "Zahlung")
		y += 13
		line := "IBAN " + m.IssuerIBAN
		if strings.TrimSpace(m.InvoiceNo) != "" {
			line += " · Verwendungszweck: " + m.InvoiceNo
		}
		gtext(pdf, marginL, y, 9.5, false, line)
		y += 14
		gtext(pdf, marginL, y, 9.5, false, "Betrag: "+money(m.Open))
	}

	gfooter(pdf, m.IssuerName, m.IssuerAddress, "", m.IssuerIBAN)
	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// wrap breaks text into lines of at most n runes on word boundaries.
func wrap(s string, n int) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		line := ""
		for _, wd := range words {
			if line == "" {
				line = wd
			} else if len([]rune(line))+1+len([]rune(wd)) <= n {
				line += " " + wd
			} else {
				out = append(out, line)
				line = wd
			}
		}
		out = append(out, line)
	}
	return out
}
