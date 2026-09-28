package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/web"
)

// TestStaticImmutableOnlyForCurrentVersion (SYN-07): a year-long immutable
// header is only safe for the bytes of this build. Another ?v= (a page of a
// newer instance reaching an older one during a rolling deploy) must revalidate,
// and every answer names the version it actually served.
func TestStaticImmutableOnlyForCurrentVersion(t *testing.T) {
	t.Parallel()
	h := http.StripPrefix("/static/", staticServer())
	for _, tc := range []struct{ name, query, want string }{
		{"current version", "?v=" + web.AssetVersion(), "public, max-age=31536000, immutable"},
		{"other version", "?v=0000000000", "no-cache"},
		{"unversioned", "", "public, max-age=3600"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/static/js/offline.js"+tc.query, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			if got := rr.Header().Get("Cache-Control"); got != tc.want {
				t.Errorf("Cache-Control = %q, want %q", got, tc.want)
			}
			if got := rr.Header().Get("X-Treckrr-Asset-Version"); got != web.AssetVersion() {
				t.Errorf("X-Treckrr-Asset-Version = %q, want %q", got, web.AssetVersion())
			}
		})
	}
}

// TestLogoutClearsOnlyTheHTTPCache (SYN-06): logout drops the HTTP cache but
// never "storage", which would also wipe other users' unsent offline bookings.
func TestLogoutClearsOnlyTheHTTPCache(t *testing.T) {
	s, _ := quickServer(t)
	rr := httptest.NewRecorder()
	s.handleLogout(rr, httptest.NewRequest(http.MethodPost, "/logout", nil))
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Clear-Site-Data"); got != `"cache"` {
		t.Fatalf("Clear-Site-Data = %q, want only \"cache\"", got)
	}
}

// TestCatalogStoreFailureIsNotAValidationMessage (SYN-04): a transient store
// failure while a booking's rig is resolved must surface as an error (500, so an
// offline replay retries), never as a message (422, a permanent rejection).
func TestCatalogStoreFailureIsNotAValidationMessage(t *testing.T) {
	s, _ := quickServer(t) // every query fails with a driver error
	for _, form := range []url.Values{
		{"gespann_id": {"3"}, "hours": {"2"}, "entry_date": {"2026-05-01"}},
		{"mode": {"manual"}, "tractor_id": {"1"}, "load_level_id": {"2"}, "hours": {"2"}, "entry_date": {"2026-05-01"}},
		{"mode": {"manual"}, "machine_ids": {"4"}, "hours": {"2"}, "entry_date": {"2026-05-01"}},
	} {
		r := httptest.NewRequest(http.MethodPost, "/entries", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		entry, _, msg, err := s.resolveUnifiedEntryFromForm(r)
		if err == nil || msg != "" || entry != nil {
			t.Errorf("%v: entry=%v msg=%q err=%v, want only an error", form, entry, msg, err)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/entries/quick", nil)
	if _, _, msg, err := s.buildGespannEntry(r, 3); err == nil || msg != "" {
		t.Errorf("quick rig: msg=%q err=%v, want only an error", msg, err)
	}
}

// TestQuickRowFingerprint (SYN-03) binds a row key to what the row books; the
// machine half ignores the helper so one can be added to a stored row later.
func TestQuickRowFingerprint(t *testing.T) {
	t.Parallel()
	base := quickRowFingerprint(1, 2, "2026-05-01", "3", "2", "")
	for name, other := range map[string]string{
		"date":     quickRowFingerprint(1, 2, "2026-05-02", "3", "2", ""),
		"gespann":  quickRowFingerprint(1, 2, "2026-05-01", "4", "2", ""),
		"hours":    quickRowFingerprint(1, 2, "2026-05-01", "3", "2,5", ""),
		"person":   quickRowFingerprint(1, 2, "2026-05-01", "3", "2", "9"),
		"account":  quickRowFingerprint(5, 2, "2026-05-01", "3", "2", ""),
		"year":     quickRowFingerprint(1, 6, "2026-05-01", "3", "2", ""),
		"sameness": base,
	} {
		if (other == base) != (name == "sameness") {
			t.Errorf("%s: fingerprint equality wrong", name)
		}
	}
	if legacyRequestFingerprint(httptest.NewRequest(http.MethodPost, "/entries", nil)) == "" {
		t.Error("legacy form lacks a retry fingerprint")
	}
}
