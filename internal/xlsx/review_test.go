package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWorkbookSheetNamesAreValidAndUnique(t *testing.T) {
	var out bytes.Buffer
	names := []string{"'Quoted'", "Quoted", "QUOTED", strings.Repeat("Ä", 35), strings.Repeat("Ä", 36), "A:B", "A?B"}
	var sheets []Sheet
	for _, name := range names {
		sheets = append(sheets, Sheet{Name: name})
	}
	if err := Write(&out, sheets...); err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(out.Bytes()), int64(out.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range z.File {
		if file.Name != "xl/workbook.xml" {
			continue
		}
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var workbook struct {
			Sheets []struct {
				Name string `xml:"name,attr"`
			} `xml:"sheets>sheet"`
		}
		if err := xml.NewDecoder(r).Decode(&workbook); err != nil {
			t.Fatal(err)
		}
		if len(workbook.Sheets) != len(names) {
			t.Fatal("missing worksheets")
		}
		seen := map[string]bool{}
		for _, sheet := range workbook.Sheets {
			name := sheet.Name
			if name == "" || utf8.RuneCountInString(name) > 31 || strings.HasPrefix(name, "'") || strings.HasSuffix(name, "'") || seen[strings.ToLower(name)] {
				t.Fatalf("invalid/duplicate name %q", name)
			}
			seen[strings.ToLower(name)] = true
		}
		return
	}
	t.Fatal("missing workbook.xml")
}
