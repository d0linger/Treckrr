package server

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestMachineQRDraftFlowIntegration proves that labels contain authenticated
// QR targets, scanning only selects a neighbor, and the existing form receives
// an explicit machine draft without creating a booking.
func TestMachineQRDraftFlowIntegration(t *testing.T) {
	e := newItEnv(t)
	labels := e.get(fmt.Sprintf("/prices/machines/labels?base=%d", e.baseID64))
	if !strings.Contains(labels, "IT-Maschine") || !strings.Contains(labels, fmt.Sprintf("/prices/machines/%d/qr.png", e.machineID)) {
		t.Fatalf("print sheet does not contain the machine label")
	}

	resp, err := e.client.Get(e.srv.URL + fmt.Sprintf("/prices/machines/%d/qr.png", e.machineID))
	if err != nil {
		t.Fatal(err)
	}
	png, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "image/png" || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
		t.Fatalf("QR response: status=%d type=%q bytes=%d", resp.StatusCode, resp.Header.Get("Content-Type"), len(png))
	}

	unauth := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = unauth.Get(e.srv.URL + fmt.Sprintf("/machines/%d/book", e.machineID))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/login") {
		t.Fatalf("unauthenticated scan = %d %q, want login redirect", resp.StatusCode, resp.Header.Get("Location"))
	}

	landing := e.get(fmt.Sprintf("/machines/%d/book", e.machineID))
	if !strings.Contains(landing, "IT-Maschine") || !strings.Contains(landing, "IT-Nachbar") ||
		!strings.Contains(landing, fmt.Sprintf("machine=%d", e.machineID)) {
		t.Fatalf("scan landing does not offer the machine and eligible neighbor")
	}
	page := e.get(fmt.Sprintf("/neighbors/%d?year=%d&machine=%d", e.neighborID, e.yearID64, e.machineID))
	checked := regexp.MustCompile(fmt.Sprintf(`name="machine_ids" value="%d"[^>]*checked`, e.machineID))
	if !strings.Contains(page, `value="manual" selected`) || !checked.MatchString(page) {
		t.Fatalf("existing booking form was not prefilled with manual mode and machine")
	}
	if strings.Contains(page, "data-entry-defaults") {
		t.Fatal("saved browser defaults may overwrite the explicit QR prefill")
	}
	var entries int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM entries WHERE billing_year_id=$1 AND neighbor_id=$2`,
		e.yearID64, e.neighborID).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("GET-only QR flow created %d entries (err=%v)", entries, err)
	}

	if err := e.st.SetMachineActive(e.ctx, e.machineID, false); err != nil {
		t.Fatal(err)
	}
	inactive := e.get(fmt.Sprintf("/machines/%d/book", e.machineID))
	if !strings.Contains(inactive, "für neue Buchungen deaktiviert") || strings.Contains(inactive, fmt.Sprintf("machine=%d", e.machineID)) {
		t.Fatal("inactive machine must resolve to a warning without a draft link")
	}
}
