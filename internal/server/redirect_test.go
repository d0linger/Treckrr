package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestIsSafeBuchungenReturnPath(t *testing.T) {
	cases := []struct {
		name string
		ret  string
		want bool
	}{
		{name: "empty string", ret: "", want: false},
		{name: "clean buchungen path", ret: "/buchungen", want: true},
		{name: "buchungen path with query", ret: "/buchungen?year=1&page=2", want: true},
		{name: "buchungen path with fragment", ret: "/buchungen#top", want: true},
		{name: "path traversal open redirect", ret: "/buchungen/../..//attacker.com", want: false},
		{name: "backslash open redirect", ret: "/buchungen\\attacker.com", want: false},
		{name: "protocol relative url", ret: "//attacker.com/buchungen", want: false},
		{name: "absolute url with host", ret: "http://attacker.com/buchungen", want: false},
		{name: "domain suffix spoofing", ret: "/buchungen.attacker.com", want: false},
		{name: "other endpoint path", ret: "/buchungen/other", want: false},
		{name: "javascript URI", ret: "javascript:alert(1)", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSafeBuchungenReturnPath(tc.ret)
			if got != tc.want {
				t.Errorf("isSafeBuchungenReturnPath(%q) = %v, want %v", tc.ret, got, tc.want)
			}
		})
	}
}
