//go:build integration

package server

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// TestXLSXExportsIntegration downloads every spreadsheet export after a real
// booking and an issued invoice: each is a workbook whose first sheet carries
// the booking's typed values, text that looks like a formula stays an inline
// string, and the CSV routes keep answering with CSV.
func TestXLSXExportsIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(e.gespannID)},
		"entry_date": {"2026-05-01"}, "hours": {"2"}, "unit": {"h"}, "task_label": {"=SUMME(A1)"}})
	e.postIssue(e.neighborID, url.Values{"year_id": {yid}})
	invoice, err := e.st.GetInvoice(e.ctx, e.yearID64, e.neighborID)
	if err != nil {
		t.Fatalf("invoice not issued: %v", err)
	}

	sheet := func(path string) string {
		t.Helper()
		resp, err := e.client.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK ||
			resp.Header.Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
			!strings.Contains(resp.Header.Get("Content-Disposition"), ".xlsx\"") {
			t.Fatalf("%s = %d %s %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"))
		}
		zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if err != nil {
			t.Fatalf("%s is not a workbook: %v", path, err)
		}
		for _, f := range zr.File {
			if f.Name == "xl/worksheets/sheet1.xml" {
				rc, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				xml, _ := io.ReadAll(rc)
				_ = rc.Close()
				return string(xml)
			}
		}
		t.Fatalf("%s has no first sheet", path)
		return ""
	}

	// 2 h × 46,00 €/h fixture rig = 92 €.
	for _, path := range []string{
		fmt.Sprintf("/export/year/%d?format=xlsx", e.yearID64),
		fmt.Sprintf("/export/neighbor/%d?year=%d&format=xlsx", e.neighborID, e.yearID64),
	} {
		got := sheet(path)
		for _, want := range []string{`t="inlineStr" s="0"><is><t xml:space="preserve">=SUMME(A1)</t>`, `<v>92</v>`, `<v>46</v>`, "Gesamt", `state="frozen"`} {
			if !strings.Contains(got, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
		if strings.Contains(got, "'=SUMME") || strings.Contains(got, "<f>") {
			t.Errorf("%s altered or evaluated the formula-looking text", path)
		}
	}
	if got := sheet("/stats/export.xlsx?year=" + yid); !strings.Contains(got, "=SUMME(A1)") || !strings.Contains(got, `<v>92</v>`) {
		t.Error("statistics workbook lacks the task and its amount")
	}
	if got := sheet("/rechnungsjournal/export.xlsx?year=" + yid); !strings.Contains(got, invoice.Number) || !strings.Contains(got, "Summe (signiert)") {
		t.Error("journal workbook lacks the invoice")
	}

	for _, path := range []string{fmt.Sprintf("/export/year/%d", e.yearID64), "/stats/export.csv?year=" + yid, "/rechnungsjournal/export.csv?year=" + yid} {
		resp, err := e.client.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/csv") {
			t.Errorf("%s no longer answers with CSV: %s", path, resp.Header.Get("Content-Type"))
		}
	}
}
