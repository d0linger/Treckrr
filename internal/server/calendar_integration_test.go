//go:build integration

package server

import (
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

var calendarLinkRe = regexp.MustCompile(`/cal/([A-Za-z0-9_-]+)\.ics`)

// TestCalendarFeedIntegration creates a calendar subscription, reads the due
// date of an open invoice from the public feed, switches to the minimal mode
// (no names) by creating a new link, which invalidates the old one, and
// finally revokes the feed.
func TestCalendarFeedIntegration(t *testing.T) {
	e := newItEnv(t)
	issuePaymentDeliveryInvoice(t, e, "100", "-44")
	public := &http.Client{Timeout: 10 * time.Second}
	fetch := func(token string) (int, string) {
		t.Helper()
		resp, err := public.Get(e.srv.URL + "/cal/" + token + ".ics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusOK && !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/calendar") {
			t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
		}
		return resp.StatusCode, strings.ReplaceAll(string(body), "\r\n ", "")
	}
	link := func(form url.Values) string {
		t.Helper()
		page := e.post("/account/calendar", form)
		m := calendarLinkRe.FindStringSubmatch(page)
		if m == nil {
			t.Fatal("calendar link not revealed on the profile")
		}
		return m[1]
	}

	first := link(url.Values{})
	status, body := fetch(first)
	if status != http.StatusOK || !strings.Contains(body, "BEGIN:VEVENT") || !strings.Contains(body, "fällig · IT-Nachbar") {
		t.Fatalf("feed: status %d, open invoice event missing", status)
	}
	if strings.Contains(e.get("/profile"), first) {
		t.Fatal("calendar link must be shown only once")
	}

	second := link(url.Values{"minimal": {"1"}})
	if status, _ = fetch(first); status != http.StatusNotFound {
		t.Fatalf("replaced link still works: %d", status)
	}
	if status, body = fetch(second); status != http.StatusOK || strings.Contains(body, "IT-Nachbar") || !strings.Contains(body, "fällig") {
		t.Fatalf("minimal feed: status %d, names leaked=%v", status, strings.Contains(body, "IT-Nachbar"))
	}

	e.post("/account/calendar/delete", url.Values{})
	if status, _ = fetch(second); status != http.StatusNotFound {
		t.Fatalf("revoked feed still works: %d", status)
	}
}
