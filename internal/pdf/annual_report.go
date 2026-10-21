package pdf

import (
	"bytes"
	"strconv"
	"time"

	"github.com/shopspring/decimal"
)

// AnnualNeighbor is one neighbor's line of the annual report.
type AnnualNeighbor struct {
	Name      string
	Entries   int
	Hours     decimal.Decimal
	Owed      decimal.Decimal // bookings plus signed account postings
	Paid      decimal.Decimal
	Remaining decimal.Decimal // negative: the neighbor holds a credit
}

// AnnualTask is one service with its booked hours and amount.
type AnnualTask struct {
	Label string
	Count int
	Hours decimal.Decimal
	Cost  decimal.Decimal
}

// AnnualMachine is one machine's utilization and, where own costs are
// maintained, its margin.
type AnnualMachine struct {
	Name      string
	Hours     decimal.Decimal
	Revenue   decimal.Decimal
	Margin    decimal.Decimal
	HasMargin bool
}

// AnnualInvoice is one issued document of the year.
type AnnualInvoice struct {
	Number, Kind, Neighbor, Status string
	IssuedOn                       time.Time
	Net, VAT, Gross                decimal.Decimal
}

// AnnualReport is the filed summary of one billing year.
type AnnualReport struct {
	IssuerName, IssuerAddress string
	Year                      int
	Completed                 bool
	Generated                 time.Time
	Bookings                  decimal.Decimal // own work, non-voided
	Hours                     decimal.Decimal
	Ledger                    decimal.Decimal // signed account postings
	Paid, Open, Credit        decimal.Decimal
	Neighbors                 []AnnualNeighbor
	Tasks                     []AnnualTask
	Machines                  []AnnualMachine
	MarginTotal               decimal.Decimal
	HasMargin                 bool
	Invoices                  []AnnualInvoice
	InvoiceNet, InvoiceVAT    decimal.Decimal
	InvoiceGross              decimal.Decimal
}

// RenderAnnualReport builds the year's report for the tax adviser or the
// members' meeting: totals, accounts per neighbor, services, machine
// utilization and margin, and the invoice journal. It reads only figures the
// application already computes; nothing is recalculated from current prices.
func RenderAnnualReport(a AnnualReport) ([]byte, error) {
	pdf, err := newDoc()
	if err != nil {
		return nil, err
	}
	pdf.AddPage()
	right := pageW - marginR
	y := 48.0
	gtext(pdf, marginL, y, 15, true, firstNonEmpty(a.IssuerName, "Jahresbericht"))
	gaccent(pdf, marginL, y+18)
	y += 24
	yIss := gblock(pdf, marginL, y, 9.5, a.IssuerAddress)
	gtextR(pdf, right, 48, 18, true, "JAHRESBERICHT "+strconv.Itoa(a.Year))
	status := "Jahr offen – vorläufige Zahlen"
	if a.Completed {
		status = "Jahr abgeschlossen"
	}
	gtextR(pdf, right, 70, 10, false, status)
	if !a.Generated.IsZero() {
		gtextR(pdf, right, 84, 9, false, "Stand: "+a.Generated.Format("02.01.2006 15:04"))
	}
	y = maxf(yIss, 96) + 18

	newPageIfNeeded := func(need float64) {
		if y+need > 770 {
			pdf.AddPage()
			y = 56
		}
	}
	heading := func(title string) {
		newPageIfNeeded(60)
		gtext(pdf, marginL, y, 11.5, true, title)
		y += 18
	}
	rule := func() {
		pdf.SetLineWidth(0.6)
		pdf.SetStrokeColor(120, 120, 120)
		pdf.Line(marginL, y, right, y)
		y += 8
	}
	row := func(label, value string, bold bool) {
		gtext(pdf, marginL, y, 9.5, bold, label)
		gtextR(pdf, marginL+380, y, 9.5, bold, value)
		y += 14
	}

	heading("Überblick")
	row("Eigene Leistungen", money(a.Bookings), false)
	row("Gebuchte Stunden", trimZeros(a.Hours)+" h", false)
	row("Verrechnung (Nachbarleistungen, Kontopositionen)", money(a.Ledger), false)
	row("Ergebnis Konten", money(a.Bookings.Add(a.Ledger)), true)
	y += 4
	row("Bezahlt", money(a.Paid), false)
	row("Noch offen", money(a.Open), false)
	if a.Credit.IsPositive() {
		row("Guthaben der Nachbarn", money(a.Credit), false)
	}
	y += 14

	if len(a.Neighbors) > 0 {
		heading("Konten je Nachbar")
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Nachbar")
			gtextR(pdf, marginL+200, y, 8.5, true, "Pos.")
			gtextR(pdf, marginL+260, y, 8.5, true, "Stunden")
			gtextR(pdf, marginL+340, y, 8.5, true, "Konto")
			gtextR(pdf, marginL+410, y, 8.5, true, "Bezahlt")
			gtextR(pdf, right, y, 8.5, true, "Offen")
			y += 12
			rule()
		}
		header()
		for _, n := range a.Neighbors {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			gtext(pdf, marginL, y, 9.5, false, clip(n.Name, 28))
			gtextR(pdf, marginL+200, y, 9.5, false, strconv.Itoa(n.Entries))
			gtextR(pdf, marginL+260, y, 9.5, false, trimZeros(n.Hours))
			gtextR(pdf, marginL+340, y, 9.5, false, money(n.Owed))
			gtextR(pdf, marginL+410, y, 9.5, false, money(n.Paid))
			if n.Remaining.IsNegative() {
				gtextR(pdf, right, y, 9, false, "Guthaben "+money(n.Remaining.Neg()))
			} else {
				gtextR(pdf, right, y, 9.5, true, money(n.Remaining))
			}
			y += 15
		}
		y += 18
	}

	if len(a.Tasks) > 0 {
		heading("Leistungen nach Tätigkeit")
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Tätigkeit")
			gtextR(pdf, marginL+300, y, 8.5, true, "Buchungen")
			gtextR(pdf, marginL+390, y, 8.5, true, "Stunden")
			gtextR(pdf, right, y, 8.5, true, "Betrag")
			y += 12
			rule()
		}
		header()
		for _, t := range a.Tasks {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			gtext(pdf, marginL, y, 9.5, false, clip(firstNonEmpty(t.Label, "Ohne Tätigkeit"), 44))
			gtextR(pdf, marginL+300, y, 9.5, false, strconv.Itoa(t.Count))
			gtextR(pdf, marginL+390, y, 9.5, false, trimZeros(t.Hours))
			gtextR(pdf, right, y, 9.5, false, money(t.Cost))
			y += 15
		}
		y += 18
	}

	if len(a.Machines) > 0 {
		heading("Maschinenauslastung")
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Maschine")
			gtextR(pdf, marginL+300, y, 8.5, true, "Stunden")
			gtextR(pdf, marginL+400, y, 8.5, true, "Erlös")
			gtextR(pdf, right, y, 8.5, true, "Deckungsbeitrag")
			y += 12
			rule()
		}
		header()
		for _, m := range a.Machines {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			gtext(pdf, marginL, y, 9.5, false, clip(m.Name, 44))
			gtextR(pdf, marginL+300, y, 9.5, false, trimZeros(m.Hours))
			gtextR(pdf, marginL+400, y, 9.5, false, money(m.Revenue))
			margin := "–"
			if m.HasMargin {
				margin = money(m.Margin)
			}
			gtextR(pdf, right, y, 9.5, false, margin)
			y += 15
		}
		if a.HasMargin {
			y += 2
			gtext(pdf, marginL, y, 9.5, true, "Deckungsbeitrag gesamt (Maschinen mit gepflegten Eigenkosten)")
			gtextR(pdf, right, y, 9.5, true, money(a.MarginTotal))
			y += 14
		}
		y += 18
	}

	heading("Rechnungsjournal")
	if len(a.Invoices) == 0 {
		gtext(pdf, marginL, y, 9.5, false, "Keine Rechnungen ausgestellt.")
		y += 14
	} else {
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Nummer · Art")
			gtext(pdf, marginL+167, y, 8.5, true, "Datum")
			gtext(pdf, marginL+220, y, 8.5, true, "Nachbar")
			gtextR(pdf, marginL+375, y, 8.5, true, "Netto")
			gtextR(pdf, marginL+440, y, 8.5, true, "USt")
			gtextR(pdf, right, y, 8.5, true, "Brutto")
			y += 12
			rule()
		}
		header()
		for _, iv := range a.Invoices {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			label := iv.Number + " · " + iv.Kind
			if iv.Status != "" {
				label += " (" + iv.Status + ")"
			}
			gtext(pdf, marginL, y, 9, false, clip(label, 32))
			gtext(pdf, marginL+167, y, 9, false, iv.IssuedOn.Format("02.01.2006"))
			gtext(pdf, marginL+220, y, 9, false, clip(iv.Neighbor, 19))
			gtextR(pdf, marginL+375, y, 9, false, money(iv.Net))
			gtextR(pdf, marginL+440, y, 9, false, money(iv.VAT))
			gtextR(pdf, right, y, 9, false, money(iv.Gross))
			y += 14
		}
		y += 2
		rule()
		gtext(pdf, marginL, y, 9.5, true, "Umsatzwirksam gesamt")
		gtextR(pdf, marginL+375, y, 9.5, true, money(a.InvoiceNet))
		gtextR(pdf, marginL+440, y, 9.5, true, money(a.InvoiceVAT))
		gtextR(pdf, right, y, 9.5, true, money(a.InvoiceGross))
		y += 14
	}

	gfooter(pdf, a.IssuerName, a.IssuerAddress, "", "")
	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
