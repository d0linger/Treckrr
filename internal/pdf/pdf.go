// Package pdf renders Treckrr documents (invoice/Beleg) as PDF, server-side, so a
// document is consistent and archivable regardless of the viewer's browser. Pure
// Go (signintech/gopdf) with the embedded Go font (goregular/gobold), so no CGo
// and no bundled binary font asset.
package pdf

import (
	"bytes"
	"strings"

	"github.com/signintech/gopdf"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
)

const (
	fontRegular = "go"
	fontBold    = "go-b"
)

// newDoc starts an A4 document with the Go fonts registered.
func newDoc() (*gopdf.GoPdf, error) {
	pdf := &gopdf.GoPdf{}
	pdf.Start(gopdf.Config{PageSize: *gopdf.PageSizeA4})
	if err := pdf.AddTTFFontData(fontRegular, goregular.TTF); err != nil {
		return nil, err
	}
	if err := pdf.AddTTFFontData(fontBold, gobold.TTF); err != nil {
		return nil, err
	}
	return pdf, nil
}

// Shared drawing helpers (used by the Mahnung and Kontoauszug renderers).

func gtext(pdf *gopdf.GoPdf, x, y, size float64, bold bool, s string) {
	f := fontRegular
	if bold {
		f = fontBold
	}
	_ = pdf.SetFont(f, "", size)
	pdf.SetXY(x, y)
	_ = pdf.Cell(nil, s)
}

// gtextR draws right-aligned text ending at x=right.
func gtextR(pdf *gopdf.GoPdf, right, y, size float64, bold bool, s string) {
	f := fontRegular
	if bold {
		f = fontBold
	}
	_ = pdf.SetFont(f, "", size)
	wdt, _ := pdf.MeasureTextWidth(s)
	pdf.SetXY(right-wdt, y)
	_ = pdf.Cell(nil, s)
}

// gblock draws a multi-line block and returns the new y.
func gblock(pdf *gopdf.GoPdf, x, y, size float64, s string) float64 {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			y += size * 0.6
			continue
		}
		gtext(pdf, x, y, size, false, strings.TrimSpace(line))
		y += size * 1.25
	}
	return y
}

// selfTest renders a page with German glyphs + € and returns the bytes; used by the
// test to prove the embedded font covers the charset we need.
func selfTest() ([]byte, error) {
	pdf, err := newDoc()
	if err != nil {
		return nil, err
	}
	pdf.AddPage()
	if err := pdf.SetFont(fontRegular, "", 14); err != nil {
		return nil, err
	}
	pdf.SetXY(40, 60)
	if err := pdf.Cell(nil, "Rechnung äöüß ÄÖÜ – 1.234,56 € · m³"); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if _, err := pdf.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
