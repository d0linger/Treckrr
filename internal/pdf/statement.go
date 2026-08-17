package pdf

import (
	"bytes"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// StatementYear is one billing year's line on the Kontoauszug.
type StatementYear struct {
	Year  int
	Cost  decimal.Decimal
	Hours decimal.Decimal
	Paid  bool
}

// StatementData is the multi-year Kontoauszug PDF's content.
type StatementData struct {
	IssuerName    string
	IssuerAddress string
	RecipientName string
	RecipientAddr string
	Rows          []StatementYear
	TotalCost     decimal.Decimal
	TotalHours    decimal.Decimal
	Today         time.Time
}

// RenderStatement builds an A4 multi-year account statement PDF.
func RenderStatement(s StatementData) ([]byte, error) {
	pdf, err := newDoc()
	if err != nil {
		return nil, err
	}
	pdf.AddPage()
	right := pageW - marginR
	y := 48.0

	gtext(pdf, marginL, y, 15, true, firstNonEmpty(s.IssuerName, "Kontoauszug"))
	gaccent(pdf, marginL, y+18)
	y += 24
	yIss := gblock(pdf, marginL, y, 9.5, s.IssuerAddress)

	gtextR(pdf, right, y, 18, true, "KONTOAUSZUG")
	if !s.Today.IsZero() {
		gtextR(pdf, right, y+22, 10, false, "Stand: "+s.Today.Format("02.01.2006"))
	}

	y = maxf(yIss, y+40) + 16
	gtext(pdf, marginL, y, 9, false, "Für")
	y += 13
	y = gblock(pdf, marginL, y, 10.5, s.RecipientName+"\n"+s.RecipientAddr) + 20

	// table
	colYear := marginL
	colHours := marginL + 300
	colPaid := marginL + 380
	gtext(pdf, colYear, y, 9, true, "Jahr")
	gtextR(pdf, colHours+30, y, 9, true, "Stunden")
	gtext(pdf, colPaid, y, 9, true, "Status")
	gtextR(pdf, right, y, 9, true, "Kosten")
	y += 13
	pdf.SetLineWidth(0.6)
	pdf.SetStrokeColor(120, 120, 120)
	pdf.Line(marginL, y, right, y)
	y += 8

	for _, r := range s.Rows {
		if y > 770 {
			pdf.AddPage()
			y = 56
		}
		gtext(pdf, colYear, y, 9.5, false, strconv.Itoa(r.Year))
		gtextR(pdf, colHours+30, y, 9.5, false, trimZeros(r.Hours)+" h")
		status := "offen"
		if r.Paid {
			status = "bezahlt"
		}
		gtext(pdf, colPaid, y, 9.5, false, status)
		gtextR(pdf, right, y, 9.5, false, money(r.Cost))
		y += 14
	}

	// Keep the summary rule + total row together above the footer if the last data
	// row ended near the page bottom.
	if y > 740 {
		pdf.AddPage()
		y = 56
	}
	y += 4
	pdf.Line(colHours, y, right, y)
	y += 10
	gtextR(pdf, colHours+30, y, 10.5, true, trimZeros(s.TotalHours)+" h")
	gtext(pdf, colPaid, y, 11, true, "Summe")
	gtextR(pdf, right, y, 11, true, money(s.TotalCost))

	gfooter(pdf, s.IssuerName, s.IssuerAddress, "", "")
	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
