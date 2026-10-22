package pdf

import (
	"bytes"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// PriceSheetLoad is one load level (column) of the tractor table.
type PriceSheetLoad struct {
	Name      string
	CostPerPS decimal.Decimal
}

// PriceSheetTractor is one tractor row with its rate per load level, in the
// order of PriceSheet.Loads.
type PriceSheetTractor struct {
	Label string
	PS    decimal.Decimal
	Rates []decimal.Decimal
}

// PriceSheetMachine is one implement with its working width and hourly rate.
type PriceSheetMachine struct {
	Name, Category string
	Width          decimal.Decimal
	CostPerAB      decimal.Decimal
	Rate           decimal.Decimal
}

// PriceSheetRig is a fixed rig with its combined hourly rate.
type PriceSheetRig struct {
	Name, Parts string
	Rate        decimal.Decimal
}

// PriceSheet is the neighbor-facing price list of one rate basis.
type PriceSheet struct {
	IssuerName, IssuerAddress string
	BaseName                  string
	ValidFrom                 int
	Today                     time.Time
	Loads                     []PriceSheetLoad
	Tractors                  []PriceSheetTractor
	Machines                  []PriceSheetMachine
	Rigs                      []PriceSheetRig
	TravelFlat, TravelPerKm   decimal.Decimal
	TaxNote                   string
}

// RenderPriceSheet builds an A4 price list: tractors by load level, machines,
// rigs and travel costs, with the validity year. It shows rates only, never
// bookings or neighbor data.
func RenderPriceSheet(p PriceSheet) ([]byte, error) {
	pdf, err := newDoc()
	if err != nil {
		return nil, err
	}
	pdf.AddPage()
	right := pageW - marginR
	y := 48.0
	gtext(pdf, marginL, y, 15, true, firstNonEmpty(p.IssuerName, "Preisblatt"))
	gaccent(pdf, marginL, y+18)
	y += 24
	yIss := gblock(pdf, marginL, y, 9.5, p.IssuerAddress)
	gtextR(pdf, right, 48, 18, true, "PREISBLATT")
	gtextR(pdf, right, 70, 10, false, "gültig ab "+strconv.Itoa(p.ValidFrom))
	if !p.Today.IsZero() {
		gtextR(pdf, right, 84, 9, false, "Stand: "+p.Today.Format("02.01.2006"))
	}
	y = maxf(yIss, 96) + 14
	gtext(pdf, marginL, y, 10, false, "Grundlage: "+p.BaseName+". Stundensätze je Einsatzstunde, Beträge in Euro.")
	y += 24

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

	if len(p.Tractors) > 0 && len(p.Loads) > 0 {
		heading("Traktoren je Belastungsstufe")
		cols := len(p.Loads)
		colW := (contentW - 200) / float64(cols)
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Traktor")
			gtextR(pdf, marginL+190, y, 8.5, true, "PS")
			for i, l := range p.Loads {
				gtextR(pdf, marginL+200+colW*float64(i+1), y, 8.5, true, l.Name)
			}
			y += 12
			rule()
		}
		header()
		for _, t := range p.Tractors {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			gtext(pdf, marginL, y, 9.5, false, clip(t.Label, 34))
			gtextR(pdf, marginL+190, y, 9.5, false, trimZeros(t.PS))
			for i, rate := range t.Rates {
				if i < cols {
					gtextR(pdf, marginL+200+colW*float64(i+1), y, 9.5, false, money(rate))
				}
			}
			y += 15
		}
		y += 4
		var costs []string
		for _, l := range p.Loads {
			costs = append(costs, l.Name+" "+trimZeros(l.CostPerPS)+" €/PS·h")
		}
		gtext(pdf, marginL, y, 8, false, "Satz = PS × Kosten je PS·h ("+strings.Join(costs, ", ")+")")
		y += 24
	}

	if len(p.Machines) > 0 {
		heading("Maschinen und Geräte")
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Maschine")
			gtext(pdf, marginL+220, y, 8.5, true, "Kategorie")
			gtextR(pdf, marginL+370, y, 8.5, true, "Breite")
			gtextR(pdf, marginL+440, y, 8.5, true, "€ je m·h")
			gtextR(pdf, right, y, 8.5, true, "Satz je h")
			y += 12
			rule()
		}
		header()
		for _, m := range p.Machines {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			gtext(pdf, marginL, y, 9.5, false, clip(m.Name, 36))
			gtext(pdf, marginL+220, y, 9, false, clip(m.Category, 22))
			gtextR(pdf, marginL+370, y, 9.5, false, trimZeros(m.Width)+" m")
			gtextR(pdf, marginL+440, y, 9.5, false, trimZeros(m.CostPerAB))
			gtextR(pdf, right, y, 9.5, true, money(m.Rate))
			y += 15
		}
		y += 20
	}

	if len(p.Rigs) > 0 {
		heading("Gespanne")
		header := func() {
			gtext(pdf, marginL, y, 8.5, true, "Gespann")
			gtext(pdf, marginL+170, y, 8.5, true, "Zusammenstellung")
			gtextR(pdf, right, y, 8.5, true, "Satz je h")
			y += 12
			rule()
		}
		header()
		for _, g := range p.Rigs {
			if y > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			lines, err := settlementTextLines(pdf, g.Parts, 9, contentW-260)
			if err != nil {
				return nil, err
			}
			if len(lines) == 0 {
				lines = []string{""}
			}
			if y+float64(min(len(lines), 50))*12+4 > 770 {
				pdf.AddPage()
				y = 56
				header()
			}
			gtext(pdf, marginL, y, 9.5, false, clip(g.Name, 28))
			gtextR(pdf, right, y, 9.5, true, money(g.Rate))
			for _, line := range lines {
				if y > 770 {
					pdf.AddPage()
					y = 56
					header()
					gtext(pdf, marginL, y, 9.5, false, clip(g.Name, 22)+" (Forts.)")
				}
				gtext(pdf, marginL+170, y, 9, false, line)
				y += 12
			}
			y += 4
		}
		y += 18
	}

	if p.TravelFlat.IsPositive() || p.TravelPerKm.IsPositive() {
		heading("Anfahrt")
		if p.TravelFlat.IsPositive() {
			gtext(pdf, marginL, y, 9.5, false, "Pauschale je Anfahrt: "+money(p.TravelFlat))
			y += 14
		}
		if p.TravelPerKm.IsPositive() {
			gtext(pdf, marginL, y, 9.5, false, "Je Kilometer: "+money(p.TravelPerKm))
			y += 14
		}
		y += 10
	}
	if p.TaxNote != "" {
		newPageIfNeeded(40)
		lines, err := settlementTextLines(pdf, p.TaxNote, 8, contentW)
		if err != nil {
			return nil, err
		}
		for _, line := range lines {
			newPageIfNeeded(11)
			gtext(pdf, marginL, y, 8, false, line)
			y += 11
		}
	}

	gfooter(pdf, p.IssuerName, p.IssuerAddress, "", "")
	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// clip shortens a cell to n runes with an ellipsis.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
