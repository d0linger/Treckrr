package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// TestWrite checks the package is complete and well-formed, text that looks
// like a formula stays an inline string, numbers and dates are typed, the
// header is frozen, and sheet names respect the spreadsheet limits.
func TestWrite(t *testing.T) {
	t.Parallel()
	d := decimal.RequireFromString
	var buf bytes.Buffer
	err := Write(&buf,
		Sheet{Name: "Buchungen 2026", Header: []string{"Nachbar", "Datum", "Stunden", "Kosten (€)"}, Rows: [][]Cell{
			{Text("=HYPERLINK(\"x\")"), Date(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)), Number(d("2.25")), Money(d("-103.5"))},
			{Text("Huber & Söhne <GesbR>\x01"), Empty(), Number(d("1")), Money(d("46"))},
		}},
		Sheet{Name: "Zu lang: ein Blattname mit [Klammern] und mehr als 31 Zeichen", Rows: [][]Cell{{Text("x")}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		files[f.Name] = string(body)
		dec := xml.NewDecoder(bytes.NewReader(body))
		for {
			if _, err := dec.Token(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("%s is not well-formed XML: %v", f.Name, err)
			}
		}
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels",
		"xl/styles.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		if _, ok := files[name]; !ok {
			t.Errorf("package lacks %s", name)
		}
	}
	sheet := files["xl/worksheets/sheet1.xml"]
	for _, want := range []string{
		`state="frozen"`, `<autoFilter ref="A1:D3"/>`,
		`<c r="A2" t="inlineStr" s="0"><is><t xml:space="preserve">=HYPERLINK(&quot;x&quot;)</t></is></c>`,
		`<c r="B2" s="2"><v>46023</v></c>`, `<c r="C2"><v>2.25</v></c>`, `<c r="D2" s="1"><v>-103.5</v></c>`,
		`Huber &amp; Söhne &lt;GesbR&gt;</t>`, `<c r="A1" t="inlineStr" s="3">`,
	} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet1 lacks %s", want)
		}
	}
	if strings.Contains(sheet, "<f>") {
		t.Error("a formula element was written")
	}
	if !strings.Contains(files["xl/workbook.xml"], `name="Zu lang_ ein Blattname mit _Kla"`) {
		t.Errorf("sheet name not sanitized: %s", files["xl/workbook.xml"])
	}
	if got := cellRef(27, 5); got != "AB5" {
		t.Errorf("cellRef(27,5) = %s, want AB5", got)
	}
}
