package server

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// members map keyed lower-case, mirroring yearMembers.
func testMembers() map[string]int64 {
	return map[string]int64{"max mustermann": 7, "anna beispiel": 9}
}

// TestParseImportCSV_SampleRoundTrips feeds the exact bytes handleImportSample
// emits (UTF-8 BOM + ';' header + German decimals) and asserts every data row
// parses, the header is skipped (not mis-parsed as a bad row), columns map to the
// right fields, and cost is recomputed Menge × Satz.
func TestParseImportCSV_SampleRoundTrips(t *testing.T) {
	csv := "\uFEFF" + // leading BOM, exactly like the export/sample
		"Nachbar;Datum;Tätigkeit;Traktor;Belastung;Maschinen;Einheit;Menge;Satz/Einheit (€);Kosten (€);Notiz\n" +
		"Max Mustermann;2026-03-14;Ballenpressen;;;;Ballen;10;3,20;32,00;Beispiel\n" +
		"Max Mustermann;2026-04-02;Mähen;;;;h;4,5;28,00;126,00;\n" +
		"Anna Beispiel;15.05.2026;Transport;;;;km;120;0,90;108,00;Silage\n"

	rows, err := parseImportCSV(csv, testMembers())
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	// 3 data rows only — the BOM'd header must be skipped, not counted.
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3 (header must be skipped despite BOM)", len(rows))
	}
	for i, r := range rows {
		if !r.OK() {
			t.Errorf("row %d (%s) not OK: %q", i, r.Neighbor, r.Err)
		}
	}

	// Row 0: unit booking, cost recomputed 10 × 3,20 = 32,00.
	r0 := rows[0]
	if r0.NeighborID != 7 || r0.Unit != "Ballen" || r0.Task != "Ballenpressen" {
		t.Errorf("row0 fields: id=%d unit=%q task=%q", r0.NeighborID, r0.Unit, r0.Task)
	}
	if !r0.Qty.Equal(decimal.RequireFromString("10")) || !r0.Price.Equal(decimal.RequireFromString("3.20")) {
		t.Errorf("row0 qty/price: %s / %s", r0.Qty, r0.Price)
	}
	if !r0.Cost.Equal(decimal.RequireFromString("32.00")) {
		t.Errorf("row0 cost = %s, want 32.00", r0.Cost)
	}
	if r0.Date.Format("2006-01-02") != "2026-03-14" {
		t.Errorf("row0 date = %s, want 2026-03-14", r0.Date.Format("2006-01-02"))
	}

	// Row 2: German date format (TT.MM.JJJJ) accepted.
	if rows[2].Date.Format("2006-01-02") != "2026-05-15" {
		t.Errorf("row2 date = %s, want 2026-05-15 (TT.MM.JJJJ)", rows[2].Date.Format("2006-01-02"))
	}
}

// TestParseImportCSV_HeaderAfterBlankLine: a leading blank record must not push
// the header onto line 2 where a line==1-only check would miss it and parse the
// header as a (rejected) data row.
func TestParseImportCSV_HeaderAfterBlankLine(t *testing.T) {
	csv := "\n" + // stray empty first record
		"Nachbar;Datum;Tätigkeit;Traktor;Belastung;Maschinen;Einheit;Menge;Satz;Kosten;Notiz\n" +
		"Max Mustermann;2026-03-14;Mähen;;;;h;2;10,00;20,00;\n"
	rows, err := parseImportCSV(csv, testMembers())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1 (blank line + header both skipped)", len(rows))
	}
	if !rows[0].OK() || rows[0].Task != "Mähen" {
		t.Errorf("row not the data line: ok=%v task=%q err=%q", rows[0].OK(), rows[0].Task, rows[0].Err)
	}
}

// TestParseImportCSV_EmptyUnitDefaultsToHours: an empty Einheit means hours.
func TestParseImportCSV_EmptyUnitDefaultsToHours(t *testing.T) {
	csv := "Max Mustermann;2026-01-02;Pflügen;;;;;5;20,00;100,00;\n"
	rows, err := parseImportCSV(csv, testMembers())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !rows[0].OK() {
		t.Fatalf("want 1 OK row, got %d (%q)", len(rows), errOf(rows))
	}
	if rows[0].Unit != "h" {
		t.Errorf("empty unit → %q, want h", rows[0].Unit)
	}
}

// TestParseImportCSV_Rejections: unknown neighbor, non-positive qty/price and a
// bad date are each rejected with a row error (not silently imported).
func TestParseImportCSV_Rejections(t *testing.T) {
	cases := map[string]struct{ line, wantErr string }{
		"unknown neighbor": {"Fremd;2026-01-02;X;;;;h;1;1,00;1,00;", "Nachbar nicht im Jahr"},
		"zero qty":         {"Max Mustermann;2026-01-02;X;;;;h;0;1,00;0,00;", "Menge muss > 0 sein"},
		"zero price":       {"Max Mustermann;2026-01-02;X;;;;h;1;0,00;0,00;", "Satz muss > 0 sein"},
		"task too long":    {"Max Mustermann;2026-01-02;" + strings.Repeat("a", 101) + ";;;;h;1;1,00;1,00;", "Tätigkeit darf höchstens 100 Zeichen lang sein."},
		"unit too long":    {"Max Mustermann;2026-01-02;Mähen;;;;" + strings.Repeat("u", 17) + ";1;1,00;1,00;", "Einheit darf höchstens 16 Zeichen lang sein."},
		"note too long":    {"Max Mustermann;2026-01-02;Mähen;;;;h;1;1,00;1,00;" + strings.Repeat("n", 501), "Notiz darf höchstens 500 Zeichen lang sein."},
		"bad date":         {"Max Mustermann;2026-13-40;X;;;;h;1;1,00;1,00;", "Datum ungültig (JJJJ-MM-TT)"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rows, err := parseImportCSV(tc.line+"\n", testMembers())
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 {
				t.Fatalf("got %d rows, want 1", len(rows))
			}
			if rows[0].Err != tc.wantErr {
				t.Errorf("err = %q, want %q", rows[0].Err, tc.wantErr)
			}
		})
	}
}

func errOf(rows []importRow) string {
	if len(rows) == 0 {
		return "(no rows)"
	}
	return rows[0].Err
}
