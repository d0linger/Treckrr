package server

import (
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestTractorAndRigQRDraftFlowIntegration extends the machine QR flow to
// tractors and rigs: both appear on the label sheet, their QR codes render,
// scanning offers the year's neighbors, and the booking form opens with the
// tractor (manual mode) or the rig (rig mode) preselected, without writing.
func TestTractorAndRigQRDraftFlowIntegration(t *testing.T) {
	e := newItEnv(t)
	labels := e.get(fmt.Sprintf("/prices/machines/labels?base=%d", e.baseID64))
	for _, want := range []string{fmt.Sprintf("/prices/tractors/%d/qr.png", e.tractorID), fmt.Sprintf("/gespanne/%d/qr.png", e.gespannID),
		"Treckrr · Traktor", "Treckrr · Gespann", "IT-Gespann"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("label sheet lacks %q", want)
		}
	}
	for _, path := range []string{fmt.Sprintf("/prices/tractors/%d/qr.png", e.tractorID), fmt.Sprintf("/gespanne/%d/qr.png", e.gespannID)} {
		resp, err := e.client.Get(e.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		png, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(png) < 8 || string(png[:8]) != "\x89PNG\r\n\x1a\n" {
			t.Fatalf("%s: status=%d bytes=%d", path, resp.StatusCode, len(png))
		}
	}

	tractorLanding := e.get(fmt.Sprintf("/tractors/%d/book", e.tractorID))
	if !strings.Contains(tractorLanding, "Traktor-QR") || !strings.Contains(tractorLanding, fmt.Sprintf("tractor=%d", e.tractorID)) {
		t.Fatal("tractor scan does not offer a prefilled draft")
	}
	page := e.get(fmt.Sprintf("/neighbors/%d?year=%d&tractor=%d", e.neighborID, e.yearID64, e.tractorID))
	tractorSelected := regexp.MustCompile(fmt.Sprintf(`<option value="%d" selected>IT-T1`, e.tractorID))
	if !strings.Contains(page, `value="manual" selected`) || !tractorSelected.MatchString(page) || !strings.Contains(page, "Traktor-QR vorausgewählt") {
		t.Fatal("booking form was not prefilled with the scanned tractor")
	}

	rigLanding := e.get(fmt.Sprintf("/gespanne/%d/book", e.gespannID))
	if !strings.Contains(rigLanding, "Gespann-QR") || !strings.Contains(rigLanding, fmt.Sprintf("gespann=%d", e.gespannID)) {
		t.Fatal("rig scan does not offer a prefilled draft")
	}
	page = e.get(fmt.Sprintf("/neighbors/%d?year=%d&gespann=%d", e.neighborID, e.yearID64, e.gespannID))
	rigSelected := regexp.MustCompile(fmt.Sprintf(`<option value="%d" selected>IT-Gespann`, e.gespannID))
	if !strings.Contains(page, `value="gespann" selected`) || !rigSelected.MatchString(page) || strings.Contains(page, "data-entry-defaults") {
		t.Fatal("booking form was not prefilled with the scanned rig")
	}

	if err := e.st.SetTractorActive(e.ctx, e.tractorID, false); err != nil {
		t.Fatal(err)
	}
	if inactive := e.get(fmt.Sprintf("/tractors/%d/book", e.tractorID)); !strings.Contains(inactive, "für neue Buchungen deaktiviert") ||
		strings.Contains(inactive, fmt.Sprintf("tractor=%d", e.tractorID)) {
		t.Fatal("inactive tractor must resolve to a warning without a draft link")
	}
	var entries int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM entries WHERE billing_year_id=$1`, e.yearID64).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("GET-only QR flow created %d entries (err=%v)", entries, err)
	}
}
