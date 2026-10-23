//go:build integration

package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/testutil/pdftext"
)

// TestAnnualReportIntegration downloads the year's report after real bookings
// and an issued invoice: the totals, the neighbor's account, the service, the
// machine and the invoice number appear, and the pages link to it. Without a
// session the report is not served.
func TestAnnualReportIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	for _, date := range []string{"2026-05-01", "2026-05-02"} {
		e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {nid}, "gespann_id": {itoa64(e.gespannID)},
			"entry_date": {date}, "hours": {"2"}, "unit": {"h"}, "task_label": {"Jahresbericht-Mähen"}})
	}
	e.postIssue(e.neighborID, url.Values{"year_id": {yid}})
	invoice, err := e.st.GetInvoice(e.ctx, e.yearID64, e.neighborID)
	if err != nil {
		t.Fatalf("invoice not issued: %v", err)
	}

	path := fmt.Sprintf("/years/%d/bericht.pdf", e.yearID64)
	resp, err := e.client.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/pdf" {
		t.Fatalf("annual report = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	text, err := pdftext.Text(body)
	if err != nil {
		t.Fatal(err)
	}
	// 2 × 2 h on the 46,00 €/h fixture rig = 184,00 €.
	for _, want := range []string{fmt.Sprintf("JAHRESBERICHT %d", e.year), "184,00 €", "4 h", "Konten je Nachbar",
		"Jahresbericht-Mähen", "IT-Maschine", invoice.Number + " · Rechnung", "Umsatzwirksam gesamt"} {
		if !strings.Contains(text, want) {
			t.Errorf("annual report lacks %q", want)
		}
	}
	if stats := e.get("/stats?year=" + yid); !strings.Contains(stats, `href="`+path+`"`) {
		t.Error("statistics page does not link the annual report")
	}

	anonymous := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = anonymous.Get(e.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Error("annual report served without a session")
	}
}
