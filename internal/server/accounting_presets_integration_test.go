//go:build integration

package server

import (
	"encoding/csv"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
)

// TestAccountingPresetsIntegration exports an invoice and its storno through
// the BMD and RZL presets: the fixed header, the date and decimal format, the
// profile's accounts and the debit/credit code follow the preset, whatever
// columns the form sent, and the page marks both as templates.
func TestAccountingPresetsIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	t.Cleanup(func() {
		_, _ = e.pool.ExecContext(e.ctx, `DELETE FROM accounting_export_profiles WHERE name LIKE 'Preset %'||$1`, e.uname)
	})
	e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(e.gespannID)},
		"entry_date": {"2026-05-01"}, "hours": {"2"}, "unit": {"h"}, "task_label": {"Mähen"}})
	e.postIssue(e.neighborID, url.Values{"year_id": {yid}})
	invoice, err := e.st.GetInvoice(e.ctx, e.yearID64, e.neighborID)
	if err != nil {
		t.Fatalf("invoice not issued: %v", err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/invoice/storno", e.neighborID), url.Values{"year_id": {yid}, "reason": {"Testfall"}})

	export := func(format string) [][]string {
		t.Helper()
		name := "Preset " + format + " " + e.uname
		e.post("/rechnungsjournal/profile", url.Values{"year_id": {yid}, "name": {name}, "format": {format},
			"revenue_account": {"4000"}, "receivable_account": {"20000"}, "tax_code": {"1"}, "cost_center": {"KS1"},
			"columns": {"number"}, "delimiter": {","}})
		var id int64
		if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM accounting_export_profiles WHERE name=$1 AND format=$2`, name, format).Scan(&id); err != nil {
			t.Fatalf("%s profile not stored: %v", format, err)
		}
		resp, err := e.client.Get(fmt.Sprintf("%s/rechnungsjournal/buchhaltung.csv?year=%s&profile=%d", e.srv.URL, yid, id))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(resp.Header.Get("Content-Disposition"), "-"+format+".csv") {
			t.Errorf("%s file name = %s", format, resp.Header.Get("Content-Disposition"))
		}
		reader := csv.NewReader(strings.NewReader(strings.TrimPrefix(string(body), "\uFEFF")))
		reader.Comma = ';'
		rows, err := reader.ReadAll()
		if err != nil {
			t.Fatalf("%s export is not semicolon CSV: %v\n%s", format, err, body)
		}
		return rows
	}

	bmd := export("bmd")
	if strings.Join(bmd[0], ";") != "satzart;konto;gkonto;belegnr;belegdatum;buchsymbol;buchcode;prozent;steuercode;betrag;steuer;text;kost" {
		t.Errorf("BMD header = %v", bmd[0])
	}
	if len(bmd) != 3 {
		t.Fatalf("BMD rows = %d, want invoice and storno: %v", len(bmd)-1, bmd)
	}
	inv, storno := bmd[1], bmd[2]
	if inv[0] != "0" || inv[1] != "20000" || inv[2] != "4000" || inv[3] != invoice.Number || inv[5] != "AR" || inv[6] != "1" ||
		inv[12] != "KS1" || !strings.Contains(inv[11], invoice.Number) || strings.Count(inv[4], ".") != 2 {
		t.Errorf("BMD invoice row = %v", inv)
	}
	if storno[6] != "2" || storno[9] != inv[9] || strings.HasPrefix(storno[9], "-") {
		t.Errorf("BMD storno must book the same positive gross on the credit side: %v", storno)
	}
	if !strings.Contains(inv[9], ",") {
		t.Errorf("BMD amount without decimal comma: %q", inv[9])
	}

	rzl := export("rzl")
	if strings.Join(rzl[0], ";") != "Belegdatum;Belegnummer;Buchungstext;Konto;Gegenkonto;SH;Betrag;Steuersatz;Steuerbetrag;Steuercode;Kostenstelle" {
		t.Errorf("RZL header = %v", rzl[0])
	}
	if len(rzl) != 3 || rzl[1][1] != invoice.Number || rzl[1][3] != "20000" || rzl[1][4] != "4000" || rzl[1][5] != "S" || rzl[2][5] != "H" {
		t.Errorf("RZL rows = %v", rzl)
	}

	if page := e.get("/rechnungsjournal?year=" + yid); !strings.Contains(page, "BMD NTCS (Buchungsimport) · Vorlage") ||
		!strings.Contains(page, "Testimport mit der Steuerberatung") {
		t.Error("journal page does not mark the presets as templates")
	}
}
