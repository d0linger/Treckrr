package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// TestDunningYearParam pins the positive int64 contract without changing valid IDs.
func TestDunningYearParam(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, raw string
		want      int64
		valid     bool
	}{
		{name: "first ID", raw: "1", want: 1, valid: true},
		{name: "trimmed ID", raw: " 42 ", want: 42, valid: true},
		{name: "largest ID", raw: "9223372036854775807", want: 9223372036854775807, valid: true},
		{name: "missing"},
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "text", raw: "year"},
		{name: "decimal", raw: "1.5"},
		{name: "overflow", raw: "9223372036854775808"},
		{name: "oversized", raw: strings.Repeat("9", 100)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?year="+url.QueryEscape(tc.raw), nil)
			got, valid := dunningYearParam(r)
			if valid != tc.valid || (valid && got != tc.want) {
				t.Fatalf("year = (%d, %v), want (%d, %v)", got, valid, tc.want, tc.valid)
			}
		})
	}
}

// TestDunningInvalidYear verifies rejection before any database or mail access.
func TestDunningInvalidYear(t *testing.T) {
	t.Parallel()
	s := testServer() // Deliberately no store: invalid input must never reach it.
	for _, route := range []struct {
		method, path string
		handler      http.HandlerFunc
	}{
		{method: http.MethodGet, path: "/neighbors/1/mahnung", handler: s.handleNeighborMahnung},
		{method: http.MethodGet, path: "/neighbors/1/mahnung.pdf", handler: s.handleMahnungPDF},
		{method: http.MethodGet, path: "/neighbors/1/mahnung/epc-qr.png", handler: s.handleMahnungEpcQR},
		{method: http.MethodPost, path: "/neighbors/1/mahnung/email", handler: s.handleMahnungEmail},
		{method: http.MethodPost, path: "/neighbors/1/mahnung/mark-sent", handler: s.handleMahnungMarkSent},
		{method: http.MethodPost, path: "/mahnwesen/batch-email", handler: s.handleMahnwesenBatchEmail},
	} {
		t.Run(route.path, func(t *testing.T) {
			for _, tc := range []struct{ name, year string }{
				{name: "missing"},
				{name: "zero", year: "0"},
				{name: "negative", year: "-5"},
				{name: "text", year: "abc"},
				{name: "decimal", year: "1.5"},
				{name: "overflow", year: "9223372036854775808"},
				{name: "oversized", year: strings.Repeat("9", 100)},
			} {
				t.Run(tc.name, func(t *testing.T) {
					values := url.Values{"year": {tc.year}}
					r := httptest.NewRequest(route.method, route.path+"?"+values.Encode(), nil)
					if route.method == http.MethodPost {
						r = httptest.NewRequest(route.method, route.path, strings.NewReader(values.Encode()))
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
					r.SetPathValue("id", "1")
					r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "dunning-validation-session"})
					r.Header.Set("X-CSRF-Token", s.csrfToken(r))
					w := httptest.NewRecorder()
					s.csrf(route.handler).ServeHTTP(w, r)
					if w.Code != http.StatusBadRequest {
						t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
					}
					if got := w.Header().Get("Location"); got != "" {
						t.Fatalf("invalid year redirected to %q", got)
					}
				})
			}
		})
	}
}
