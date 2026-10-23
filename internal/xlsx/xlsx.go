// Package xlsx writes small Office Open XML spreadsheets: typed number, money
// and date cells, a bold frozen header row with an autofilter, and text as
// inline strings. An inline string is never evaluated, so a cell starting with
// "=" stays text and formula injection is impossible by construction.
package xlsx

import (
	"archive/zip"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

type kind int

const (
	kindEmpty kind = iota
	kindText
	kindNumber
	kindMoney
	kindDate
)

// Cell is one typed spreadsheet value.
type Cell struct {
	kind kind
	text string
	num  decimal.Decimal
	date time.Time
}

// Text is a literal string cell.
func Text(s string) Cell { return Cell{kind: kindText, text: s} }

// Number is a numeric cell in the general format.
func Number(d decimal.Decimal) Cell { return Cell{kind: kindNumber, num: d} }

// Money is a numeric cell shown with two decimals and thousands separators.
func Money(d decimal.Decimal) Cell { return Cell{kind: kindMoney, num: d} }

// Date is a calendar-day cell shown as DD.MM.YYYY.
func Date(t time.Time) Cell { return Cell{kind: kindDate, date: t} }

// Empty is a blank cell.
func Empty() Cell { return Cell{} }

// Sheet is one worksheet: a header row and typed data rows.
type Sheet struct {
	Name   string
	Header []string
	Rows   [][]Cell
}

// Style indexes into the cellXfs of styles.xml below.
const (
	styleDefault = 0
	styleMoney   = 1
	styleDate    = 2
	styleHeader  = 3
)

// Write streams a workbook with the given sheets.
func Write(w io.Writer, sheets ...Sheet) error {
	if len(sheets) == 0 {
		return fmt.Errorf("xlsx: no sheets")
	}
	z := zip.NewWriter(w)
	names := make([]string, len(sheets))
	used := make(map[string]bool, len(sheets))
	for i, s := range sheets {
		base := sheetName(s.Name, i)
		name := base
		for n := 2; used[strings.ToLower(name)]; n++ {
			suffix := fmt.Sprintf(" (%d)", n)
			runes := []rune(base)
			name = strings.TrimRight(string(runes[:min(len(runes), 31-len(suffix))]), "'") + suffix
		}
		used[strings.ToLower(name)] = true
		names[i] = name
	}
	files := []struct {
		name string
		body func(io.Writer) error
	}{
		{"[Content_Types].xml", func(out io.Writer) error { return contentTypes(out, len(sheets)) }},
		{"_rels/.rels", constant(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`)},
		{"xl/workbook.xml", func(out io.Writer) error { return workbook(out, names) }},
		{"xl/_rels/workbook.xml.rels", func(out io.Writer) error { return workbookRels(out, len(sheets)) }},
		{"xl/styles.xml", constant(styles)},
	}
	for _, f := range files {
		fw, err := z.Create(f.name)
		if err != nil {
			return err
		}
		if err := f.body(fw); err != nil {
			return err
		}
	}
	for i, s := range sheets {
		fw, err := z.Create(fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1))
		if err != nil {
			return err
		}
		if err := worksheet(fw, s); err != nil {
			return err
		}
	}
	return z.Close()
}

func constant(body string) func(io.Writer) error {
	return func(out io.Writer) error {
		_, err := io.WriteString(out, body)
		return err
	}
}

func contentTypes(out io.Writer, sheets int) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	for i := 1; i <= sheets; i++ {
		fmt.Fprintf(&b, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i)
	}
	b.WriteString(`</Types>`)
	_, err := io.WriteString(out, b.String())
	return err
}

func workbook(out io.Writer, names []string) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	for i, name := range names {
		fmt.Fprintf(&b, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, escape(name), i+1, i+1)
	}
	b.WriteString(`</sheets></workbook>`)
	_, err := io.WriteString(out, b.String())
	return err
}

func workbookRels(out io.Writer, sheets int) error {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= sheets; i++ {
		fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i, i)
	}
	fmt.Fprintf(&b, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`, sheets+1)
	_, err := io.WriteString(out, b.String())
	return err
}

// styles: 0 default, 1 money (#,##0.00), 2 date (DD.MM.YYYY), 3 bold header.
const styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><numFmts count="2"><numFmt numFmtId="164" formatCode="#,##0.00"/><numFmt numFmtId="165" formatCode="DD.MM.YYYY"/></numFmts><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="4"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/><xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="165" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/><xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`

func worksheet(out io.Writer, s Sheet) error {
	cols := len(s.Header)
	for _, row := range s.Rows {
		cols = max(cols, len(row))
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0">`)
	if len(s.Header) > 0 {
		b.WriteString(`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>`)
	}
	b.WriteString(`</sheetView></sheetViews>`)
	if cols > 0 {
		b.WriteString(`<cols>`)
		for i := 0; i < cols; i++ {
			fmt.Fprintf(&b, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, columnWidth(s, i))
		}
		b.WriteString(`</cols>`)
	}
	b.WriteString(`<sheetData>`)
	rowNum := 1
	if len(s.Header) > 0 {
		fmt.Fprintf(&b, `<row r="1">`)
		for i, h := range s.Header {
			writeCell(&b, cellRef(i, 1), Text(h), styleHeader)
		}
		b.WriteString(`</row>`)
		rowNum++
	}
	for _, row := range s.Rows {
		fmt.Fprintf(&b, `<row r="%d">`, rowNum)
		for i, c := range row {
			writeCell(&b, cellRef(i, rowNum), c, -1)
		}
		b.WriteString(`</row>`)
		rowNum++
	}
	b.WriteString(`</sheetData>`)
	if len(s.Header) > 0 {
		fmt.Fprintf(&b, `<autoFilter ref="A1:%s"/>`, cellRef(len(s.Header)-1, max(rowNum-1, 1)))
	}
	b.WriteString(`</worksheet>`)
	_, err := io.WriteString(out, b.String())
	return err
}

func writeCell(b *strings.Builder, ref string, c Cell, style int) {
	switch c.kind {
	case kindText:
		if style < 0 {
			style = styleDefault
		}
		fmt.Fprintf(b, `<c r="%s" t="inlineStr" s="%d"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, escape(c.text))
	case kindNumber:
		fmt.Fprintf(b, `<c r="%s"><v>%s</v></c>`, ref, c.num.String())
	case kindMoney:
		fmt.Fprintf(b, `<c r="%s" s="%d"><v>%s</v></c>`, ref, styleMoney, c.num.String())
	case kindDate:
		if c.date.IsZero() {
			return
		}
		fmt.Fprintf(b, `<c r="%s" s="%d"><v>%d</v></c>`, ref, styleDate, serialDay(c.date))
	}
}

// serialDay is the spreadsheet day number of a calendar date (1900 system,
// whose epoch is 1899-12-30 once Excel's phantom 29.02.1900 is accounted for).
func serialDay(t time.Time) int {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return int(day.Sub(time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)).Hours() / 24)
}

// cellRef converts a zero-based column and a one-based row to "A1" notation.
func cellRef(col, row int) string {
	name := ""
	for col >= 0 {
		name = string(rune('A'+col%26)) + name
		col = col/26 - 1
	}
	return fmt.Sprintf("%s%d", name, row)
}

// columnWidth estimates a readable width from the longest value, bounded.
func columnWidth(s Sheet, col int) int {
	width := 8
	measure := func(text string) {
		width = max(width, utf8.RuneCountInString(text)+2)
	}
	if col < len(s.Header) {
		measure(s.Header[col])
	}
	for _, row := range s.Rows {
		if col >= len(row) {
			continue
		}
		switch c := row[col]; c.kind {
		case kindText:
			measure(c.text)
		case kindNumber, kindMoney:
			measure(c.num.StringFixed(2) + "   ")
		case kindDate:
			measure("00.00.0000")
		}
	}
	return min(width, 60)
}

// sheetName applies the spreadsheet limits: at most 31 characters and none
// of : \ / ? * [ ].
func sheetName(name string, i int) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`:\/?*[]`, r) {
			return '_'
		}
		return r
	}, strings.Trim(strings.TrimSpace(name), "'"))
	if r := []rune(name); len(r) > 31 {
		name = string(r[:31])
	}
	name = strings.Trim(name, "'")
	if name == "" {
		name = fmt.Sprintf("Tabelle%d", i+1)
	}
	return name
}

// escape writes XML text, dropping characters XML 1.0 cannot carry.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\t' || r == '\n' || r == '\r' || (r >= 0x20 && r <= 0xD7FF) || (r >= 0xE000 && r <= 0xFFFD) || r >= 0x10000:
			if r == utf8.RuneError {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}
