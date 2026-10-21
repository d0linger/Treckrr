//go:build integration

package server

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

// signaturePNG returns a pad-like data URL: a transparent 600×200 canvas with
// a stroke, or an empty canvas when blank.
func signaturePNG(t *testing.T, blank bool) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 600, 200))
	if !blank {
		for x := 60; x < 540; x++ {
			for y := 95; y < 101; y++ {
				img.Set(x, y+(x%40)/4, color.NRGBA{A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestEntrySignatureIntegration pins the on-site signature end to end: it is
// stored with a new own booking in the same transaction (online and through
// the offline replay, once), unusable pads are refused without booking
// anything, the Beleg and the edit page show it, a later edit is flagged, and
// an existing booking can be signed again after the signature was removed.
func TestEntrySignatureIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := itoa64(e.neighborID), itoa64(e.yearID64)
	signedAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	form := func(date, name, pad string) url.Values {
		return url.Values{"booking_form_version": {"3"}, "booking_kind": {"equipment"}, "booking_direction": {"out"},
			"year_id": {yid}, "neighbor_id": {nid}, "entry_date": {date}, "mode": {"gespann"},
			"gespann_id": {itoa64(e.gespannID)}, "hours": {"1"},
			"signature_name": {name}, "signature_png": {pad}, "signature_at": {signedAt}}
	}
	byDate := func() map[string]models.Entry {
		t.Helper()
		entries, err := e.st.ListEntries(e.ctx, e.neighborID, e.yearID64)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]models.Entry{}
		for _, en := range entries {
			out[en.Date.Format("2006-01-02")] = en
		}
		return out
	}

	// Unusable pads book nothing.
	e.post("/entries", form("2026-06-01", "Hans Huber", signaturePNG(t, true)))
	e.post("/entries", form("2026-06-02", "", signaturePNG(t, false)))
	e.post("/entries", form("2026-06-03", "Hans Huber", "data:image/gif;base64,R0lGODlhAQABAAAAACw="))
	if n := len(byDate()); n != 0 {
		t.Fatalf("%d bookings stored from unusable signatures, want 0", n)
	}

	e.post("/entries", form("2026-06-10", "Hans Huber", signaturePNG(t, false)))
	signed, ok := byDate()["2026-06-10"]
	if !ok {
		t.Fatal("signed booking missing")
	}
	sig, err := e.st.GetEntrySignature(e.ctx, signed.ID)
	if err != nil {
		t.Fatalf("signature not stored with the booking: %v", err)
	}
	if sig.SignerName != "Hans Huber" || len(sig.Image) == 0 || sig.ConfirmedText != models.SignatureSummary(signed) ||
		sig.SignedAt.After(sig.ReceivedAt.Add(-50*time.Minute)) {
		t.Errorf("signature = %q, %d bytes, confirmed %q (want %q), signed %s received %s",
			sig.SignerName, len(sig.Image), sig.ConfirmedText, models.SignatureSummary(signed), sig.SignedAt, sig.ReceivedAt)
	}

	// The offline queue replays the same POST: stored once, then deduplicated.
	offline := form("2026-06-11", "Eva Huber", signaturePNG(t, false))
	offline.Set("idempotency_key", e.uname+"-sig")
	for i := 0; i < 2; i++ {
		if res := e.replay("/entries", offline, true); res.status != http.StatusNoContent {
			t.Fatalf("replay %d = %d %s", i, res.status, res.body)
		}
	}
	replayed := byDate()["2026-06-11"]
	if sig, err := e.st.GetEntrySignature(e.ctx, replayed.ID); err != nil || sig.SignerName != "Eva Huber" {
		t.Errorf("replayed signature = %+v, %v", sig, err)
	}

	if beleg := e.get("/neighbors/" + nid + "/beleg?year=" + yid); !strings.Contains(beleg, "vor Ort unterschrieben: Hans Huber") {
		t.Error("Beleg does not show the signature")
	}
	edit := e.get("/entries/" + itoa64(signed.ID) + "/edit")
	if !strings.Contains(edit, `/entries/`+itoa64(signed.ID)+`/signature.png`) || !strings.Contains(edit, "Bestätigt: "+sig.ConfirmedText) ||
		strings.Contains(edit, "data-signature-changed") {
		t.Error("edit page does not show the signature and what it confirmed")
	}
	res, err := e.client.Get(e.srv.URL + "/entries/" + itoa64(signed.ID) + "/signature.png")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" {
		t.Errorf("signature image = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}

	// A booking changed after signing is flagged on its edit page.
	if _, err := e.pool.ExecContext(e.ctx, `UPDATE entries SET hours=2, quantity=2, cost=cost*2 WHERE id=$1`, signed.ID); err != nil {
		t.Fatal(err)
	}
	if edit := e.get("/entries/" + itoa64(signed.ID) + "/edit"); !strings.Contains(edit, "data-signature-changed") {
		t.Error("a booking edited after signing is not flagged")
	}

	// Remove and sign again on the edit page.
	e.post("/entries/"+itoa64(signed.ID)+"/signature/delete", url.Values{})
	if _, err := e.st.GetEntrySignature(e.ctx, signed.ID); err == nil {
		t.Fatal("signature not removed")
	}
	e.post("/entries/"+itoa64(signed.ID)+"/signature", url.Values{"signature_name": {"Hans Huber"}, "signature_png": {signaturePNG(t, false)}})
	again, err := e.st.GetEntrySignature(e.ctx, signed.ID)
	if err != nil || !strings.Contains(again.ConfirmedText, "· 2 h ·") {
		t.Errorf("re-signed signature = %+v, %v; want it to confirm the edited 2 h", again, err)
	}

	if export := e.get("/neighbors/" + nid + "/dsgvo-export.json"); !strings.Contains(export, `"signer_name": "Eva Huber"`) {
		t.Error("DSGVO export does not list the signatures")
	}
}
