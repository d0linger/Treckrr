//go:build integration

package server

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/config"
)

// TestRetentionAssistantIntegration lists only archived neighbors whose last
// activity lies before the retention cutoff, reminds administrators once a
// year, and anonymizes through the existing typed confirmation, returning to
// the list. Nothing is anonymized on its own.
func TestRetentionAssistantIntegration(t *testing.T) {
	e := newItEnv(t)
	yid := itoa64(e.yearID64)
	book := func(neighborID int64, date string) {
		e.post("/entries", url.Values{"year_id": {yid}, "neighbor_id": {itoa64(neighborID)},
			"gespann_id": {itoa64(e.gespannID)}, "entry_date": {date}, "hours": {"1"}, "unit": {"h"}})
	}
	old := e.secondNeighbor("Alt-Archiv")
	recent := e.secondNeighbor("Neu-Archiv")
	active := e.secondNeighbor("Alt-Aktiv")
	book(old, "2010-05-01")
	book(recent, time.Now().Format("2006-01-02"))
	book(active, "2010-05-02")
	for _, id := range []int64{old, recent} {
		if err := e.st.SetNeighborArchived(e.ctx, id, true); err != nil {
			t.Fatal(err)
		}
	}

	page := e.get(retentionPath)
	if !strings.Contains(page, "Alt-Archiv "+e.uname) || !strings.Contains(page, "Letzte Aktivität 01.05.2010") {
		t.Error("an archived neighbor past the retention period is not listed")
	}
	if strings.Contains(page, "Neu-Archiv "+e.uname) || strings.Contains(page, "Alt-Aktiv "+e.uname) {
		t.Error("a recent or still active neighbor is offered for anonymization")
	}

	// The yearly reminder reaches the administrator once per calendar year.
	srv := &Server{cfg: &config.Config{RetentionYears: 7}, store: e.st}
	notice := srv.retentionNotice(e.ctx)
	if notice == nil || notice.Kind != "privacy" || !notice.AdminOnly || notice.Href != retentionPath ||
		notice.DedupeKey != "retention:"+strconv.Itoa(time.Now().Year()) {
		t.Fatalf("retention notice = %+v", notice)
	}
	if err := e.st.RefreshNotifications(e.ctx, notice); err != nil {
		t.Fatalf("a privacy notice was not stored: %v", err)
	}
	var stored int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM notifications n JOIN users u ON u.id = n.user_id
		WHERE u.username = $1 AND n.kind = 'privacy' AND n.dedupe_key = $2`, e.uname, notice.DedupeKey).Scan(&stored); err != nil || stored != 1 {
		t.Errorf("admin privacy notifications = %d (%v), want 1", stored, err)
	}

	// Without the typed word nothing happens; with it the neighbor is gone
	// from the list and the operator lands back on it.
	e.post("/neighbors/"+itoa64(old)+"/anonymize", url.Values{"back": {retentionPath}, "confirm": {"ja"}})
	if n, _ := e.st.GetNeighbor(e.ctx, old); n == nil || n.Anonymized {
		t.Fatal("anonymized without the typed confirmation")
	}
	resp, err := e.client.PostForm(e.srv.URL+"/neighbors/"+itoa64(old)+"/anonymize",
		url.Values{"csrf_token": {e.csrf(retentionPath)}, "back": {retentionPath}, "confirm": {"ANONYMISIEREN"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if n, _ := e.st.GetNeighbor(e.ctx, old); n == nil || !n.Anonymized {
		t.Fatal("the assistant's anonymize did not run")
	}
	if !strings.HasSuffix(resp.Request.URL.Path, retentionPath) {
		t.Errorf("anonymize returned to %s, want the retention list", resp.Request.URL.Path)
	}
	if page := e.get(retentionPath); strings.Contains(page, "Alt-Archiv "+e.uname) {
		t.Error("an anonymized neighbor is still listed")
	}
}
