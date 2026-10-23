package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// releaseVersion is the release this binary was built as. The Dockerfile sets
// it from the git tag through -ldflags; local builds keep "".
var releaseVersion = ""

// updateCheckInterval spaces the optional release lookups; the maintenance
// tick runs more often and simply skips until the interval has passed.
const updateCheckInterval = 24 * time.Hour

// updateState holds the latest release lookup in memory only.
type updateState struct {
	mu        sync.Mutex
	attempted time.Time
	checked   time.Time
	latest    string
	url       string
	published time.Time
	err       string
}

// updateView is what the system page shows about releases.
type updateView struct {
	Enabled   bool
	Current   string
	Latest    string
	URL       string
	Published time.Time
	Checked   time.Time
	Newer     bool
	Error     string
}

// semver parses "v1.2.3" or "1.2.3" (pre-release suffixes ignored) into its
// three numbers.
func semver(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// newerRelease reports whether latest is a higher version than current. An
// unparsable current version (a development build) never claims an update.
func newerRelease(current, latest string) bool {
	c, okC := semver(current)
	l, okL := semver(latest)
	if !okC || !okL {
		return false
	}
	for i := range 3 {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

// CheckForUpdate looks up the newest release at most once per interval when
// UPDATE_CHECK is on. It sends nothing but the request itself and stores only
// the tag, link and date in memory.
func (s *Server) CheckForUpdate(ctx context.Context) {
	if s.cfg == nil || !s.cfg.UpdateCheck {
		return
	}
	s.updates.mu.Lock()
	if time.Since(s.updates.attempted) < updateCheckInterval {
		s.updates.mu.Unlock()
		return
	}
	s.updates.attempted = time.Now()
	s.updates.mu.Unlock()

	tag, link, published, err := fetchLatestRelease(ctx, s.cfg.UpdateCheckURL)
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	if err != nil {
		s.updates.err = err.Error()
		slog.Warn("update check failed", "err", sanitizeLog(err.Error()))
		return
	}
	s.updates.err = ""
	s.updates.checked, s.updates.latest, s.updates.url, s.updates.published = time.Now(), tag, link, published
}

// fetchLatestRelease reads a GitHub-style "latest release" document.
func fetchLatestRelease(ctx context.Context, url string) (tag, link string, published time.Time, err error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", time.Time{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Treckrr-update-check")
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // G107: operator-configured https URL, validated at startup
	if err != nil {
		return "", "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", time.Time{}, fmt.Errorf("release feed answered %d", resp.StatusCode)
	}
	var doc struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return "", "", time.Time{}, fmt.Errorf("release feed unreadable: %w", err)
	}
	if doc.Draft || doc.Prerelease || doc.TagName == "" {
		return "", "", time.Time{}, fmt.Errorf("release feed has no stable release")
	}
	if !strings.HasPrefix(doc.HTMLURL, "https://") {
		doc.HTMLURL = ""
	}
	return doc.TagName, doc.HTMLURL, doc.PublishedAt, nil
}

// updateStatus summarizes the in-memory lookup for the system page.
func (s *Server) updateStatus() updateView {
	v := updateView{Current: buildLabel()}
	if s.cfg == nil || !s.cfg.UpdateCheck {
		return v
	}
	s.updates.mu.Lock()
	defer s.updates.mu.Unlock()
	v.Enabled = true
	v.Latest, v.URL, v.Published, v.Checked, v.Error = s.updates.latest, s.updates.url, s.updates.published, s.updates.checked, s.updates.err
	v.Newer = newerRelease(v.Current, v.Latest)
	return v
}
