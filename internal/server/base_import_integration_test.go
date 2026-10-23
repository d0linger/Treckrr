//go:build integration

package server

import (
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// TestRateListRoundTripIntegration exports the environment's basis, edits one
// rate and adds a machine, previews the import against the original, and
// commits it: a new basis carries the edited values and the rig, while the
// original basis is left untouched.
func TestRateListRoundTripIntegration(t *testing.T) {
	e := newItEnv(t)
	csvText := e.get(fmt.Sprintf("/bases/%d/export.csv", e.baseID64))
	if !strings.HasPrefix(csvText, "\xef\xbb\xbfArt;Kennung;Name;PS;") {
		t.Fatalf("export header: %q", csvText[:min(len(csvText), 60)])
	}
	for _, want := range []string{"Belastungsstufe;;it-mittel;;0,36;", "Traktor;IT-T1;IT-Traktor;100;", "Maschine;;IT-Maschine;;;2;5;IT;0;ja"} {
		if !strings.Contains(csvText, want) {
			t.Fatalf("export lacks %q:\n%s", want, csvText)
		}
	}
	edited := strings.Replace(csvText, "it-mittel;;0,36;", "it-mittel;;0,4;", 1) + "Maschine;;IT-Schwader;;;4;2,5;IT;;ja\r\n"

	// The base forms accept "gültig ab" years up to 3000, below this package's
	// year band, so the imported basis gets its own year and explicit cleanup.
	const importYear = 2999
	importName := "IT-Import " + e.uname
	t.Cleanup(func() {
		if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM price_bases WHERE name=$1`, importName); err != nil {
			t.Errorf("cleanup imported basis: %v", err)
		}
	})
	query := fmt.Sprintf("?year=%d&source_base_id=%d&name=%s", importYear, e.baseID64, url.QueryEscape(importName))
	preview := html.UnescapeString(e.postMultipart("/bases/import/preview"+query, "liste.csv", []byte(edited)))
	for _, want := range []string{"1 neu", "1 geändert", "2 unverändert", "Kosten je PS·h 0,36 → 0,4 (+11,1 %)", "IT-Schwader", "Grundlage 2999 anlegen"} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview lacks %q", want)
		}
	}

	broken := strings.Replace(edited, "IT-T1;IT-Traktor;100;", "IT-T1;IT-Traktor;0;", 1)
	if page := e.postMultipart("/bases/import/preview"+query, "liste.csv", []byte(broken)); !strings.Contains(page, "PS fehlen") ||
		strings.Contains(page, `action="/bases/import"`) {
		t.Fatal("an invalid row must be reported and block the import")
	}

	bases, err := e.st.ListBases(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := len(bases)
	e.post("/bases/import", url.Values{
		"year": {"2999"}, "name": {importName}, "source_base_id": {itoa64(e.baseID64)}, "csv": {edited},
	})
	bases, err = e.st.ListBases(e.ctx)
	if err != nil || len(bases) != before+1 {
		t.Fatalf("bases %d → %d, err=%v", before, len(bases), err)
	}
	var newID int64
	for _, b := range bases {
		if b.Name == importName && b.Year == importYear {
			newID = b.ID
		}
	}
	if newID == 0 {
		t.Fatal("imported basis not found")
	}
	loads, _ := e.st.ListLoadLevels(e.ctx, newID)
	machines, _ := e.st.ListMachines(e.ctx, newID)
	gespanne, _ := e.st.ListGespanne(e.ctx, newID)
	if len(loads) != 1 || !loads[0].CostPerPS.Equal(decimal.RequireFromString("0.4")) {
		t.Fatalf("imported loads %+v", loads)
	}
	if len(machines) != 2 || len(gespanne) != 1 || gespanne[0].Name != "IT-Gespann" || len(gespanne[0].MachineIDs) != 1 {
		t.Fatalf("imported machines=%d gespanne=%+v", len(machines), gespanne)
	}
	original, _ := e.st.ListLoadLevels(e.ctx, e.baseID64)
	if len(original) != 1 || !original[0].CostPerPS.Equal(decimal.RequireFromString("0.36")) {
		t.Fatalf("original basis changed: %+v", original)
	}
	var audited int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM audit_log WHERE action='import' AND entity='base' AND entity_id=$1`,
		itoa64(newID)).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("import audit rows=%d err=%v", audited, err)
	}
}
