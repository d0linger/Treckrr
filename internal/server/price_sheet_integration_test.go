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

// TestPriceSheetIntegration downloads the price list of the env's basis as a
// signed-in user and through a running statement link, which stops working
// once the link is revoked.
func TestPriceSheetIntegration(t *testing.T) {
	e := newItEnv(t)
	fetch := func(client *http.Client, path string) (int, string) {
		t.Helper()
		resp, err := client.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, ""
		}
		if resp.Header.Get("Content-Type") != "application/pdf" {
			t.Fatalf("%s: content type %q", path, resp.Header.Get("Content-Type"))
		}
		text, err := pdftext.Text(body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, text
	}
	status, text := fetch(e.client, fmt.Sprintf("/bases/%d/preisblatt.pdf", e.baseID64))
	if status != http.StatusOK {
		t.Fatalf("price sheet: %d", status)
	}
	// IT-Traktor 100 PS × 0,36 = 36,00 €/h; IT-Maschine 2 m × 5 = 10,00 €/h; the rig adds both.
	for _, want := range []string{"PREISBLATT", "IT-T1 IT-Traktor", "36,00 €", "IT-Maschine", "10,00 €", "IT-Gespann", "46,00 €"} {
		if !strings.Contains(text, want) {
			t.Errorf("price sheet lacks %q", want)
		}
	}

	created := e.post(fmt.Sprintf("/neighbors/%d/zwischenstand/share?year=%d", e.neighborID, e.yearID64), url.Values{"days": {"7"}})
	match := statementLinkRe.FindStringSubmatch(created)
	if match == nil {
		t.Fatal("statement link not revealed")
	}
	public := &http.Client{Timeout: 10 * time.Second}
	if status, text = fetch(public, "/s/konto/"+match[1]+"/preisblatt.pdf"); status != http.StatusOK || !strings.Contains(text, "IT-Gespann") {
		t.Fatalf("shared price sheet: status %d", status)
	}
	shares, err := e.st.ListStatementShares(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(shares) != 1 {
		t.Fatalf("shares=%v err=%v", shares, err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/zwischenstand/revoke?year=%d", e.neighborID, e.yearID64), url.Values{"share_id": {itoa64(shares[0].ID)}})
	if status, _ = fetch(public, "/s/konto/"+match[1]+"/preisblatt.pdf"); status != http.StatusNotFound {
		t.Fatalf("revoked link still serves the price sheet: %d", status)
	}
}
