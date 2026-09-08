package server

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/url"
	"strings"
	"testing"
	"time"
)

// pngBytes builds a tiny valid PNG the photo pipeline can decode.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		for y := 0; y < 8; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 30), G: uint8(y * 30), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// postFiles uploads several files under one field name, which is what the
// multi-photo upload (Ausbaukarte 75) has to accept.
func (e *itEnv) postFiles(path, field string, files map[string][]byte) string {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("csrf_token", e.csrf("/"))
	for name, content := range files {
		fw, err := mw.CreateFormFile(field, name)
		if err != nil {
			e.t.Fatal(err)
		}
		_, _ = fw.Write(content)
	}
	_ = mw.Close()
	resp, err := e.client.Post(e.srv.URL+path, mw.FormDataContentType(), &buf)
	if err != nil {
		e.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		e.t.Fatalf("POST %s -> %d", path, resp.StatusCode)
	}
	return string(b)
}

// Photos become visible outside the edit page, and several upload at once
// (Ausbaukarte 74/75).
func TestPhotoVisibilityIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-11-01"},
		"hours": {"1"}, "unit": {"h"},
	})
	entries, err := e.st.ListEntries(e.ctx, nid, yid)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries: %v (n=%d)", err, len(entries))
	}
	eid := entries[0].ID

	// Two images in ONE round-trip.
	img := pngBytes(t)
	e.postFiles(fmt.Sprintf("/entries/%d/photos", eid), "photo", map[string][]byte{
		"schein1.png": img, "schein2.png": img,
	})
	counts, err := e.st.PhotoCounts(e.ctx, yid, nid)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if counts[eid] != 2 {
		t.Fatalf("photo count = %d, want 2 from one multi-upload", counts[eid])
	}

	// The chip and the gallery show up on the neighbor page, and the bookings
	// list carries the chip too.
	page := e.get(fmt.Sprintf("/neighbors/%d?year=%d", nid, yid))
	if !strings.Contains(page, "2 Fotos") {
		t.Errorf("neighbor page shows no photo chip")
	}
	if !strings.Contains(page, "Belegfotos (2)") {
		t.Errorf("neighbor page shows no gallery")
	}
	if page := e.get(fmt.Sprintf("/buchungen?year=%d", yid)); !strings.Contains(page, "2 Fotos") {
		t.Errorf("bookings list shows no photo chip")
	}
}

// The audit trail can be narrowed by user and date range (Ausbaukarte 73).
func TestAuditFiltersIntegration(t *testing.T) {
	e := newItEnv(t)
	// The login alone produced audit rows for this user.
	page := e.get("/admin/audit?username=" + url.QueryEscape(e.uname))
	if !strings.Contains(page, e.uname) {
		t.Fatalf("user filter hides the user's own entries")
	}
	// A different user must yield nothing of ours.
	if page := e.get("/admin/audit?username=" + url.QueryEscape(e.uname+"-gibtsnicht")); strings.Contains(page, "Seite 1/1") && strings.Contains(page, e.uname) {
		t.Errorf("filtering by an unknown user still shows our entries")
	}
	// A window that ends before today must exclude today's rows.
	past := time.Now().AddDate(0, 0, -30).Format("2006-01-02")
	old := time.Now().AddDate(0, 0, -60).Format("2006-01-02")
	page = e.get(fmt.Sprintf("/admin/audit?username=%s&from=%s&to=%s", url.QueryEscape(e.uname), old, past))
	if !strings.Contains(page, "Keine Einträge.") {
		t.Errorf("a 30-day-old window still reports today's entries")
	}
	// Today's window includes them again.
	today := time.Now().Format("2006-01-02")
	page = e.get(fmt.Sprintf("/admin/audit?username=%s&from=%s&to=%s", url.QueryEscape(e.uname), today, today))
	if strings.Contains(page, "Keine Einträge.") {
		t.Errorf("today's window reports no entries although the user just logged in")
	}
}

// The neighbor list separates active from archived (Ausbaukarte 78) and the
// overview finally lists the payments it promised (79).
func TestNeighborScopeAndHistoryIntegration(t *testing.T) {
	e := newItEnv(t)
	nid, yid := e.neighborID, e.yearID64
	name := "IT-Nachbar " + e.uname

	if page := e.get("/neighbors"); !strings.Contains(page, name) {
		t.Fatalf("the active neighbor is missing from the default view")
	}
	e.post(fmt.Sprintf("/neighbors/%d/archive", nid), url.Values{"archived": {"true"}})
	if page := e.get("/neighbors"); strings.Contains(page, name) {
		t.Errorf("an archived neighbor still shows in the active view")
	}
	if page := e.get("/neighbors?scope=archiviert"); !strings.Contains(page, name) {
		t.Errorf("the archived view does not list the archived neighbor")
	}
	if page := e.get("/neighbors?scope=alle"); !strings.Contains(page, name) {
		t.Errorf("the all view does not list the archived neighbor")
	}
	e.post(fmt.Sprintf("/neighbors/%d/archive", nid), url.Values{"archived": {"false"}})

	// Two payments in the year, then the cross-year overview must show them.
	e.post("/entries", url.Values{
		"year_id": {itoa64(yid)}, "neighbor_id": {itoa64(nid)},
		"gespann_id": {itoa64(e.gespannID)}, "entry_date": {"2026-11-02"},
		"hours": {"2"}, "unit": {"h"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"30"}, "paid_on": {"2026-11-03"}, "method": {"bar"},
	})
	e.post(fmt.Sprintf("/neighbors/%d/payments", nid), url.Values{
		"year_id": {itoa64(yid)}, "amount": {"12,50"}, "paid_on": {"2026-11-04"}, "note": {"Rest"},
	})
	page := e.get(fmt.Sprintf("/neighbors/%d/overview", nid))
	for _, want := range []string{"30,00", "12,50", "42,50", "bar", "Rest"} {
		if !strings.Contains(page, want) {
			t.Errorf("payment history is missing %q", want)
		}
	}
}
