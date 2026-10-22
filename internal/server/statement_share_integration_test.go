//go:build integration

package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var statementLinkRe = regexp.MustCompile(`/s/konto/([A-Za-z0-9_-]+)`)

// TestStatementShareIntegration follows a running statement link from creation
// to revocation: the public page shows the open work exactly as the invoice
// would and hides notes, the raw link is revealed once, an objection is frozen
// with its line and raises a writer notification, a stale line choice and a
// revoked link are refused, and nothing anonymous can create a link.
func TestStatementShareIntegration(t *testing.T) {
	e := newItEnv(t)
	e.post("/entries", url.Values{
		"year_id": {itoa64(e.yearID64)}, "neighbor_id": {itoa64(e.neighborID)}, "entry_date": {fmt.Sprintf("%d-05-12", e.year)},
		"unit": {"Pauschale"}, "quantity": {"1"}, "unit_price": {"120"},
		"task_label": {"SICHTBARE LEISTUNG"}, "note": {"PRIVATE NOTIZ"},
	})
	account := fmt.Sprintf("/neighbors/%d?year=%d", e.neighborID, e.yearID64)
	created := e.post(fmt.Sprintf("/neighbors/%d/zwischenstand/share?year=%d", e.neighborID, e.yearID64), url.Values{"days": {"7"}})
	match := statementLinkRe.FindStringSubmatch(created)
	if match == nil || !strings.Contains(created, "nur einmal sichtbar") {
		t.Fatal("created link was not revealed on the account page")
	}
	token := match[1]
	if again := e.get(account); strings.Contains(again, token) || !strings.Contains(again, "Gültig bis") {
		t.Fatal("raw link must be shown once while the active link stays listed")
	}

	public := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	fetch := func(method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var resp *http.Response
		var err error
		if method == http.MethodGet {
			resp, err = public.Get(e.srv.URL + path)
		} else {
			resp, err = public.PostForm(e.srv.URL+path, form)
		}
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Location"), string(body)
	}
	status, _, page := fetch(http.MethodGet, "/s/konto/"+token, nil)
	if status != http.StatusOK {
		t.Fatalf("public statement: status %d", status)
	}
	for _, want := range []string{"Zwischenstand", "SICHTBARE LEISTUNG", "Noch nicht abgerechnete Leistungen", "120,00", "Das ist noch keine Rechnung"} {
		if !strings.Contains(page, want) {
			t.Errorf("public statement lacks %q", want)
		}
	}
	for _, forbidden := range []string{"PRIVATE NOTIZ", "Abmelden", "/entries/"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("public statement leaks %q", forbidden)
		}
	}
	for _, result := range []string{"confirmed", "disputed", "invalid", ""} {
		_, _, feedbackPage := fetch(http.MethodGet, "/s/konto/"+token+"?feedback="+result, nil)
		want := result == "confirmed" || result == "disputed"
		if got := strings.Contains(feedbackPage, "Danke, die Rückmeldung wurde gespeichert."); got != want {
			t.Errorf("feedback=%q: confirmation shown=%v, want %v", result, got, want)
		}
	}

	status, _, _ = fetch(http.MethodPost, "/s/konto/"+token+"/feedback", url.Values{"status": {"disputed"}, "line": {"99"}, "message": {"Falsche Zeile"}})
	if status != http.StatusBadRequest {
		t.Fatalf("stale line choice: status %d, want 400", status)
	}
	status, location, _ := fetch(http.MethodPost, "/s/konto/"+token+"/feedback", url.Values{"status": {"disputed"}, "line": {"0"}, "message": {"Nur eine Stunde"}})
	if status != http.StatusSeeOther || location != "/s/konto/"+token+"?feedback=disputed" {
		t.Fatalf("objection: status %d location %q", status, location)
	}
	overview := e.get(account)
	if !strings.Contains(overview, "Einwand") || !strings.Contains(overview, "SICHTBARE LEISTUNG") || !strings.Contains(overview, "Nur eine Stunde") {
		t.Fatal("operator does not see the frozen objection")
	}
	if err := e.st.RefreshNotifications(e.ctx, nil); err != nil {
		t.Fatal(err)
	}
	var notices int
	if err := e.pool.QueryRowContext(e.ctx, `SELECT count(*) FROM notifications n JOIN users u ON u.id=n.user_id
		WHERE u.username=$1 AND n.kind='feedback' AND n.href=$2`, e.uname,
		fmt.Sprintf("/neighbors/%d?year=%d", e.neighborID, e.yearID64)).Scan(&notices); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("objection raised %d writer notifications, want 1", notices)
	}

	shares, err := e.st.ListStatementShares(e.ctx, e.neighborID, e.yearID64)
	if err != nil || len(shares) != 1 {
		t.Fatalf("shares=%v err=%v", shares, err)
	}
	e.post(fmt.Sprintf("/neighbors/%d/zwischenstand/revoke?year=%d", e.neighborID, e.yearID64), url.Values{"share_id": {itoa64(shares[0].ID)}})
	if status, _, _ = fetch(http.MethodGet, "/s/konto/"+token, nil); status != http.StatusNotFound {
		t.Fatalf("revoked link: status %d, want 404", status)
	}
	if status, location, _ = fetch(http.MethodPost, fmt.Sprintf("/neighbors/%d/zwischenstand/share?year=%d", e.neighborID, e.yearID64), url.Values{"days": {"7"}}); status != http.StatusSeeOther || !strings.HasPrefix(location, "/login") {
		t.Fatalf("anonymous create: status %d location %q", status, location)
	}
	if status, _ := e.status(http.MethodPost, fmt.Sprintf("/neighbors/%d/zwischenstand/share", e.neighborID), url.Values{"days": {"7"}}); status != http.StatusBadRequest {
		t.Fatalf("create without explicit year: status %d, want 400", status)
	}
}
