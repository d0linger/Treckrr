package server

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/d0linger/treckrr/internal/models"
)

// TestParseRateListMapsColumnsByHeader reads a list whose columns were
// reordered in a spreadsheet, with a BOM and German decimals, and reports
// each invalid line instead of rejecting the whole file.
func TestParseRateListMapsColumnsByHeader(t *testing.T) {
	t.Parallel()
	raw := "\xef\xbb\xbfName;Art;Kosten je PS·h;PS;Kennung;Arbeitsbreite (m);Kosten je m·h;Aktiv\n" +
		"mittel;Belastungsstufe;0,36;;;;;\n" +
		"Allrad 120;Traktor;;120;T1;;;ja\n" +
		"Mähwerk;Maschine;;;;3;6,50;nein\n" +
		"Ohne PS;Traktor;;0;T2;;;\n" +
		"MÄHWERK;Maschine;;;;2;5;\n" +
		"Pflug;Gerät;;;;1,5;x;\n" +
		"Etwas;Sonstiges;;;;;;\n" +
		"\n"
	rows, fileErr := parseRateList([]byte(raw))
	if fileErr != "" {
		t.Fatal(fileErr)
	}
	if len(rows) != 7 {
		t.Fatalf("rows=%d, want 7", len(rows))
	}
	load, tractor, machine := rows[0], rows[1], rows[2]
	if load.Kind != "load" || !load.CostPerPS.Equal(decimal.RequireFromString("0.36")) || load.Err != "" {
		t.Errorf("load row: %+v", load)
	}
	if tractor.Kind != "tractor" || tractor.Ident != "T1" || !tractor.PS.Equal(decimal.NewFromInt(120)) || !tractor.Active || tractor.Err != "" {
		t.Errorf("tractor row: %+v", tractor)
	}
	if machine.Kind != "machine" || !machine.CostPerAB.Equal(decimal.RequireFromString("6.5")) || machine.Active || machine.Err != "" {
		t.Errorf("machine row: %+v", machine)
	}
	for i, want := range map[int]string{3: "PS fehlen", 4: "Doppelt, bereits in Zeile 4", 5: "Kosten je m·h", 6: "Unbekannte Art"} {
		if !strings.Contains(rows[i].Err, want) {
			t.Errorf("row %d err=%q, want %q", i, rows[i].Err, want)
		}
	}
}

// TestParseRateListRejectsUnusableFiles names the file-level problems.
func TestParseRateListRejectsUnusableFiles(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"no header":   "Traktor;T1;Allrad;120\n",
		"header only": "Art;Name\n",
		"too large":   strings.Repeat("x", maxRateListBytes+1),
	} {
		if _, fileErr := parseRateList([]byte(raw)); fileErr == "" {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestDiffRateListAgainstSource classifies rows and lists dropped items, with
// the relative change of rates and the resulting machine rate.
func TestDiffRateListAgainstSource(t *testing.T) {
	t.Parallel()
	d := decimal.RequireFromString
	rows, fileErr := parseRateList([]byte("Art;Kennung;Name;PS;Kosten je PS·h;Arbeitsbreite (m);Kosten je m·h\n" +
		"Belastungsstufe;;mittel;;0,38\n" +
		"Traktor;T1;Allrad;120\n" +
		"Maschine;;Mähwerk;;;3;6\n" +
		"Maschine;;Schwader;;;4;2,5\n"))
	if fileErr != "" {
		t.Fatal(fileErr)
	}
	p := diffRateList(rows,
		[]models.LoadLevel{{Name: "mittel", CostPerPS: d("0.36")}, {Name: "schwer", CostPerPS: d("0.40")}},
		[]models.Tractor{{Ident: "t1", Name: "Allrad", PS: d("120"), Active: true}},
		[]models.Machine{{Name: "Mähwerk", WorkingWidth: d("3"), CostPerAB: d("5"), Active: true}})
	if p.New != 1 || p.Changed != 2 || p.Same != 1 || p.Errors != 0 || !p.Valid {
		t.Fatalf("counts new=%d changed=%d same=%d errors=%d", p.New, p.Changed, p.Same, p.Errors)
	}
	if len(p.Removed) != 1 || p.Removed[0].Key != "schwer" {
		t.Fatalf("removed=%+v", p.Removed)
	}
	if got := strings.Join(p.Rows[0].Changes, "|"); got != "Kosten je PS·h 0,36 → 0,38 (+5,6 %)" {
		t.Errorf("load change %q", got)
	}
	if got := strings.Join(p.Rows[2].Changes, "|"); !strings.Contains(got, "Kosten je m·h 5 → 6 (+20,0 %)") || !strings.Contains(got, "Maschinensatz €/h 15 → 18 (+20,0 %)") {
		t.Errorf("machine change %q", got)
	}
	if p.Rows[3].Status != "neu" || p.Rows[1].Status != "gleich" {
		t.Errorf("statuses %q %q", p.Rows[3].Status, p.Rows[1].Status)
	}
}
