//go:build integration

package server

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

var hiddenFieldRe = regexp.MustCompile(`<input type="hidden" name="(year|name|source_base_id|csv)" value="([^"]*)">`)

// TestBaseUpliftIntegration raises the env basis' load-level rates by 10 %
// and leaves machines alone: the preview shows only that change, and the
// commit creates a new basis with the raised rate and the copied rig while the
// source basis keeps its values.
func TestBaseUpliftIntegration(t *testing.T) {
	e := newItEnv(t)
	name := "IT-Uplift " + e.uname
	t.Cleanup(func() {
		if _, err := e.pool.ExecContext(e.ctx, `DELETE FROM price_bases WHERE name=$1`, name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	preview := e.post("/bases/uplift/preview", url.Values{
		"source_base_id": {itoa64(e.baseID64)}, "year": {"2999"}, "name": {name},
		"pct_load": {"10"}, "pct_machine": {""}, "round": {"0.01"},
	})
	plain := html.UnescapeString(preview)
	for _, want := range []string{"1 geändert", "Kosten je PS·h 0,36 → 0,4 (+11,1 %)", "Grundlage 2999 anlegen"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("uplift preview lacks %q", want)
		}
	}
	if strings.Contains(plain, "Kosten je m·h 5 →") {
		t.Fatal("machines must stay unchanged without a machine percentage")
	}
	form := url.Values{}
	for _, m := range hiddenFieldRe.FindAllStringSubmatch(preview, -1) {
		form.Set(m[1], html.UnescapeString(m[2]))
	}
	if form.Get("csv") == "" {
		t.Fatal("preview form carries no rate list")
	}
	e.post("/bases/import", form)

	var newID int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM price_bases WHERE name=$1`, name).Scan(&newID); err != nil {
		t.Fatalf("uplifted basis not created: %v", err)
	}
	loads, _ := e.st.ListLoadLevels(e.ctx, newID)
	machines, _ := e.st.ListMachines(e.ctx, newID)
	rigs, _ := e.st.ListGespanne(e.ctx, newID)
	if len(loads) != 1 || !loads[0].CostPerPS.Equal(decimal.RequireFromString("0.4")) {
		t.Fatalf("uplifted loads %+v", loads)
	}
	if len(machines) != 1 || !machines[0].CostPerAB.Equal(decimal.NewFromInt(5)) || len(rigs) != 1 {
		t.Fatalf("machines=%+v rigs=%d", machines, len(rigs))
	}
	original, _ := e.st.ListLoadLevels(e.ctx, e.baseID64)
	if !original[0].CostPerPS.Equal(decimal.RequireFromString("0.36")) {
		t.Fatal("source basis changed")
	}
}
