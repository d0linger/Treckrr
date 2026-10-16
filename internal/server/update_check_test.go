package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
)

// TestNewerRelease compares release tags and never claims an update for a
// development build.
func TestNewerRelease(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		current, latest string
		want            bool
	}{
		{"v1.4.2", "v1.5.0", true},
		{"v1.4.2", "1.4.10", true},
		{"v1.4.2", "v1.4.2", false},
		{"v1.5.0", "v1.4.9", false},
		{"v2.0.0-rc1", "v2.0.0", false},
		{"dev", "v9.9.9", false},
		{"abc1234567*", "v1.0.0", false},
		{"v1.4.2", "latest", false},
	} {
		if got := newerRelease(tc.current, tc.latest); got != tc.want {
			t.Errorf("newerRelease(%q, %q) = %v, want %v", tc.current, tc.latest, got, tc.want)
		}
	}
}

// TestCheckForUpdate reads a release feed once per interval, ignores drafts
// and pre-releases, keeps errors visible and stays silent when switched off.
func TestCheckForUpdate(t *testing.T) {
	calls := 0
	body := `{"tag_name":"v9.0.0","html_url":"https://example.invalid/release","published_at":"2026-10-01T10:00:00Z"}`
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("User-Agent") != "Treckrr-update-check" {
			t.Errorf("unexpected user agent %q", r.Header.Get("User-Agent"))
		}
		_, _ = w.Write([]byte(body))
	}))
	defer feed.Close()

	off := &Server{cfg: &config.Config{UpdateCheckURL: feed.URL}}
	off.CheckForUpdate(context.Background())
	if calls != 0 || off.updateStatus().Enabled {
		t.Fatal("update check ran although UPDATE_CHECK is off")
	}

	previous := releaseVersion
	releaseVersion = "v1.4.2"
	t.Cleanup(func() { releaseVersion = previous })
	s := &Server{cfg: &config.Config{UpdateCheck: true, UpdateCheckURL: feed.URL}}
	s.CheckForUpdate(context.Background())
	s.CheckForUpdate(context.Background())
	if calls != 1 {
		t.Fatalf("feed called %d times within one interval, want 1", calls)
	}
	v := s.updateStatus()
	if !v.Newer || v.Latest != "v9.0.0" || v.URL != "https://example.invalid/release" || v.Current != "v1.4.2" {
		t.Fatalf("status %+v", v)
	}

	for name, doc := range map[string]string{
		"draft":      `{"tag_name":"v9.1.0","draft":true}`,
		"prerelease": `{"tag_name":"v9.1.0","prerelease":true}`,
		"garbage":    `not json`,
	} {
		body = doc
		fresh := &Server{cfg: &config.Config{UpdateCheck: true, UpdateCheckURL: feed.URL}}
		fresh.CheckForUpdate(context.Background())
		if got := fresh.updateStatus(); got.Newer || got.Error == "" {
			t.Errorf("%s: status %+v, want an error and no update", name, got)
		}
	}
}
