//go:build integration

package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestShareTargetIntegration covers the server side of photo sharing: a share
// POST that reached the server (no service worker yet) is accepted without a
// CSRF token, changes nothing and lands on the explanation; the picker lists
// open bookings as upload targets for writers.
func TestShareTargetIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)}, "entry_date": {fmt.Sprintf("%d-05-12", e.year)},
		"unit": {"Pauschale"}, "quantity": {"1"}, "unit_price": {"80"}, "task_label": {"FOTO-ZIEL"},
	})
	var entryID int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM entries WHERE billing_year_id=$1 AND task_label='FOTO-ZIEL'`, e.yearID64).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	resp, err := e.client.PostForm(e.srv.URL+"/share-target", url.Values{"photos": {"not a file"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Request.URL.Path != "/share-target" || resp.Request.URL.RawQuery != "fallback=1" {
		t.Fatalf("share POST without token: status %d at %s", resp.StatusCode, resp.Request.URL)
	}
	page := e.get("/share-target")
	if !strings.Contains(page, fmt.Sprintf(`value="/entries/%d/photos"`, entryID)) || !strings.Contains(page, "FOTO-ZIEL") || !strings.Contains(page, `name="csrf_token"`) {
		t.Fatal("picker does not offer the open booking with a protected form")
	}
	var photos int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM entry_photos WHERE entry_id=$1`, entryID).Scan(&photos); err != nil || photos != 0 {
		t.Fatalf("share POST stored %d photos (err=%v)", photos, err)
	}
}
