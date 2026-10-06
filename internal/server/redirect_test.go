package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRedirectOnlyAllowsSameOriginAbsolutePaths(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		target string
		want   string
	}{
		{name: "local path", target: "/neighbors/7", want: "/neighbors/7"},
		{name: "query and fragment", target: "/buchungen?year=3#top", want: "/buchungen?year=3#top"},
		{name: "escaped path", target: "/neighbors/Muster%20Hof", want: "/neighbors/Muster%20Hof"},
		{name: "empty", target: "", want: "/"},
		{name: "relative path", target: "login", want: "/"},
		{name: "absolute URL", target: "https://attacker.example/phish", want: "/"},
		{name: "scheme without host", target: "javascript:alert(1)", want: "/"},
		{name: "protocol relative", target: "//attacker.example/phish", want: "/"},
		{name: "triple slash", target: "///attacker.example/phish", want: "/"},
		{name: "backslash authority", target: `/\attacker.example/phish`, want: "/"},
		{name: "encoded slash authority", target: "/%2f%2fattacker.example/phish", want: "/"},
		{name: "encoded backslash authority", target: "/%5cattacker.example/phish", want: "/"},
		{name: "decoded control", target: "/%09/attacker.example", want: "/"},
		{name: "raw control", target: "/safe\r\nLocation: https://attacker.example", want: "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := httptest.NewRequest(http.MethodPost, "http://treckrr.test/action", nil)
			w := httptest.NewRecorder()

			redirect(w, r, tc.target)

			if w.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
			}
			if got := w.Header().Get("Location"); got != tc.want {
				t.Errorf("Location = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEntryBulkReturnPath(t *testing.T) {
	s := testServer()
	for _, tc := range []struct {
		name, target string
		allowed      bool
	}{
		{name: "list", target: "/buchungen", allowed: true},
		{name: "filters", target: "/buchungen?year=1&page=2", allowed: true},
		{name: "anchor", target: "/buchungen#top", allowed: true},
		{name: "empty"},
		{name: "traversal", target: "/buchungen/../..//attacker.example"},
		{name: "backslash", target: `/buchungen\attacker.example`},
		{name: "protocol relative", target: "//attacker.example/buchungen"},
		{name: "absolute", target: "https://attacker.example/buchungen"},
		{name: "suffix", target: "/buchungen.attacker.example"},
		{name: "child", target: "/buchungen/other"},
		{name: "encoded traversal", target: "/buchungen/%2e%2e//attacker.example"},
		{name: "encoded backslash", target: "/buchungen%5cattacker.example"},
		{name: "encoded slash", target: "/%2fbuchungen"},
		{name: "canonicalized path", target: "/other/../buchungen"},
		{name: "invalid escape", target: "/buchungen%zz"},
		{name: "control character", target: "/buchungen\r\nLocation: https://attacker.example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"year_id": {"1"}, "return_to": {tc.target}}
			req := httptest.NewRequest(http.MethodPost, "/entries/bulk", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			s.handleEntryBulk(rr, req)
			want := "/buchungen?year=1"
			if tc.allowed {
				want = tc.target
			}
			if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != want {
				t.Fatalf("response = %d %q, want redirect to %q", rr.Code, rr.Header().Get("Location"), want)
			}
		})
	}
}

func TestSafeReturnPath(t *testing.T) {
	cases := []struct {
		name     string
		referer  string
		fallback string
		want     string
	}{
		{
			name:     "empty referer",
			referer:  "",
			fallback: "/profile",
			want:     "/profile",
		},
		{
			name:     "legitimate same-origin path",
			referer:  "/profile",
			fallback: "/fallback",
			want:     "/profile",
		},
		{
			name:     "legitimate same-origin path with query",
			referer:  "/profile?foo=bar",
			fallback: "/fallback",
			want:     "/profile?foo=bar",
		},
		{
			name:     "different host absolute url",
			referer:  "http://attacker.com/profile",
			fallback: "/fallback",
			want:     "/fallback",
		},
		{
			name:     "protocol relative url",
			referer:  "//attacker.com/profile",
			fallback: "/fallback",
			want:     "/fallback",
		},
		{
			name:     "triple slash protocol relative url",
			referer:  "///attacker.com/profile",
			fallback: "/fallback",
			want:     "/fallback",
		},
		{
			name:     "backslash open redirect bypass 1",
			referer:  "/\\attacker.com/profile",
			fallback: "/fallback",
			want:     "/fallback",
		},
		{
			name:     "backslash open redirect bypass 2",
			referer:  "\\attacker.com/profile",
			fallback: "/fallback",
			want:     "/fallback",
		},
		{
			name:     "backslash in middle of path",
			referer:  "/foo\\bar",
			fallback: "/fallback",
			want:     "/fallback",
		},
		{
			name:     "invalid URL referer",
			referer:  "://invalid",
			fallback: "/fallback",
			want:     "/fallback",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://localhost:8080/theme", nil)
			if tc.referer != "" {
				r.Header.Set("Referer", tc.referer)
			}
			got := safeReturnPath(r, tc.fallback)
			if got != tc.want {
				t.Errorf("safeReturnPath(r, %q) with Referer %q = %q, want %q", tc.fallback, tc.referer, got, tc.want)
			}
		})
	}
}
