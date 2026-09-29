package server

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestSavedBookingViewsIntegration verifies views are user-owned, allowlisted
// and reusable without retaining attacker-supplied paths or unknown keys.
func TestSavedBookingViewsIntegration(t *testing.T) {
	e := newItEnv(t)
	raw := url.Values{
		"year": {itoa64(e.yearID64)}, "kind": {"equipment"}, "direction": {"out"},
		"task": {"Heuernte"}, "evil": {"/admin/backup"},
	}.Encode()
	e.post("/views/bookings", url.Values{
		"year_id": {itoa64(e.yearID64)}, "name": {"Meine Maschinen"}, "query": {raw},
	})
	var userID int64
	if err := e.pool.QueryRowContext(e.ctx, `SELECT id FROM users WHERE username=$1`, e.uname).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	views, err := e.st.ListSavedViews(e.ctx, userID, "bookings")
	if err != nil || len(views) != 1 {
		t.Fatalf("saved views = %+v, %v", views, err)
	}
	if strings.Contains(views[0].Query, "evil") || !strings.Contains(views[0].Query, "kind=equipment") {
		t.Fatalf("stored query was not allowlisted: %q", views[0].Query)
	}
	page := e.get("/buchungen?" + views[0].Query)
	if !strings.Contains(page, "Meine Maschinen") {
		t.Fatal("saved view is not rendered on the booking list")
	}
	e.post(fmt.Sprintf("/views/%d/delete", views[0].ID), url.Values{})
	if views, _ = e.st.ListSavedViews(e.ctx, userID, "bookings"); len(views) != 0 {
		t.Fatalf("view was not deleted: %+v", views)
	}
}

// TestNeighborCommunicationIntegration verifies the timeline combines existing
// delivery and public-link records without reading mail bodies or tokens.
func TestNeighborCommunicationIntegration(t *testing.T) {
	e := newItEnv(t)
	if _, err := e.pool.ExecContext(e.ctx, `
		INSERT INTO beleg_sends (billing_year_id,neighbor_id,channel) VALUES ($1,$2,'persönlich')`,
		e.yearID64, e.neighborID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateBelegShare(e.ctx, "timeline-token-hash", e.neighborID, e.yearID64,
		time.Now().Add(24*time.Hour), e.uname); err != nil {
		t.Fatal(err)
	}
	events, err := e.st.NeighborCommunication(e.ctx, e.neighborID, 20)
	if err != nil || len(events) < 2 {
		t.Fatalf("communication = %+v, %v", events, err)
	}
	page := e.get(fmt.Sprintf("/neighbors/%d/overview", e.neighborID))
	for _, want := range []string{"Kommunikationschronik", "Beleg übergeben", "Freigabelink erstellt", "persönlich"} {
		if !strings.Contains(page, want) {
			t.Errorf("timeline page missing %q", want)
		}
	}
}
