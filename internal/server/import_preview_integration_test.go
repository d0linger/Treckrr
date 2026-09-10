package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// The import preview gained per-line selection and an editable CSV
// (Ausbaukarte 69). Both are checked against what actually lands in the
// database, not just against what the page renders.
func TestImportPreviewSelectionAndCorrectionIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	name := "IT-Nachbar " + e.uname
	header := "Nachbar;Datum;Tätigkeit;Traktor;Belastung;Maschinen;Einheit;Menge;Satz/Einheit (€);Kosten (€);Notiz\n"

	// Three rows: two good, one for an unknown neighbor.
	broken := header +
		name + ";2026-10-01;Mähen;;;;h;2;20,00;40,00;erste\n" +
		"Gibt Es Nicht;2026-10-02;Mähen;;;;h;1;20,00;20,00;kaputt\n" +
		name + ";2026-10-03;Pressen;;;;Ballen;10;3,00;30,00;dritte\n"

	// The preview accepts the CSV as text (the correction editor's path), not
	// only as an upload.
	page := e.post("/entries/import/preview", url.Values{
		"year_id": {itoa64(yid)}, "csv": {broken},
	})
	if !strings.Contains(page, "2 von 3 Zeilen importierbar") {
		t.Fatalf("preview did not report 2 of 3 importable rows")
	}
	if !strings.Contains(page, "Zeilen korrigieren") {
		t.Errorf("preview offers no correction editor")
	}

	token := extractValue(t, page, "import_token")

	// Import only line 2 (the first data row). Lines are 1-based over the file
	// including the header, so the good rows are 2 and 4.
	e.post("/entries/import", url.Values{
		"year_id": {itoa64(yid)}, "csv": {broken},
		"import_token": {token}, "line": {"2"},
	})
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("selection imported %d rows, want exactly the 1 ticked", len(entries))
	}
	if entries[0].Cost.StringFixed(2) != "40.00" {
		t.Errorf("imported the wrong line: cost %s, want 40.00", entries[0].Cost.StringFixed(2))
	}

	// Correct the broken row and re-check: now all three are importable.
	fixed := strings.Replace(broken, "Gibt Es Nicht", name, 1)
	page = e.post("/entries/import/preview", url.Values{
		"year_id": {itoa64(yid)}, "csv": {fixed},
	})
	if !strings.Contains(page, "3 von 3 Zeilen importierbar") {
		t.Fatalf("the corrected CSV was not re-checked as fully importable")
	}

	// Import the two remaining lines with a fresh token; the already-imported
	// one is a separate token+line, so it is added again — which is correct:
	// the operator chose those lines.
	token = extractValue(t, page, "import_token")
	e.post("/entries/import", url.Values{
		"year_id": {itoa64(yid)}, "csv": {fixed},
		"import_token": {token}, "line": {"3", "4"},
	})
	entries, _ = e.st.ListEntries(e.ctx, nid, yid)
	if len(entries) != 3 {
		t.Fatalf("after importing 2 more lines there are %d bookings, want 3", len(entries))
	}

	// A re-submit of the same token+lines must be a no-op (one-shot import).
	e.post("/entries/import", url.Values{
		"year_id": {itoa64(yid)}, "csv": {fixed},
		"import_token": {token}, "line": {"3", "4"},
	})
	if again, _ := e.st.ListEntries(e.ctx, nid, yid); len(again) != 3 {
		t.Errorf("re-submitting the same import created duplicates (%d)", len(again))
	}
}

// extractValue pulls the value of a hidden input out of a rendered page.
func extractValue(t *testing.T, page, name string) string {
	t.Helper()
	marker := `name="` + name + `" value="`
	i := strings.Index(page, marker)
	if i < 0 {
		t.Fatalf("no %s field on the page", name)
	}
	rest := page[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		t.Fatalf("unterminated %s value", name)
	}
	return rest[:j]
}

// A quick sanity walk over the pages P12 added, so a template error in them
// shows up as a failing test rather than a 500 in the barn.
func TestNewPagesRenderIntegration(t *testing.T) {
	e := newItEnv(t)
	for _, path := range []string{
		"/buchungen?year=" + itoa64(e.yearID64),
		"/personen",
		"/recurring",
		fmt.Sprintf("/years/%d/abschluss", e.yearID64),
		fmt.Sprintf("/rechnungsjournal?year=%d", e.yearID64),
		fmt.Sprintf("/years/%d/issue-all", e.yearID64),
		"/entries/import?year=" + itoa64(e.yearID64),
	} {
		if body := e.get(path); !strings.Contains(body, "</html>") {
			t.Errorf("%s did not render a complete page", path)
		}
	}
}
