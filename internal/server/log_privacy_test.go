package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
)

func TestShareTokenLogRedaction(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	s := &Server{cfg: &config.Config{}}
	token := strings.Repeat("a", 64)
	for _, path := range []string{"/s/beleg/" + token, "/s/beleg/" + token + "/extra"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		s.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(httptest.NewRecorder(), r)
	}
	if strings.Contains(output.String(), token) {
		t.Fatal("bearer token leaked into access log")
	}
	if !strings.Contains(output.String(), "/s/beleg/[redacted]") {
		t.Fatal("redacted route missing")
	}
	if logRequestPath("/entries/12") != "/entries/12" {
		t.Fatal("ordinary route changed")
	}
}

func TestCSPReportDoesNotLogUntrustedURLsOrSamples(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	s := &Server{cfg: &config.Config{}}
	secret := strings.Repeat("b", 64)
	for _, directive := range []string{"script-src-elem", secret} {
		body := `{"csp-report":{"document-uri":"https://example.invalid/s/beleg/` + secret +
			`","effective-directive":"` + directive + `","blocked-uri":"` + secret +
			`","script-sample":"` + secret + `","line-number":3,"column-number":5}}`
		r := httptest.NewRequest(http.MethodPost, "/csp-report", strings.NewReader(body))
		r.Header.Set("User-Agent", secret)
		w := httptest.NewRecorder()
		s.handleCSPReport(w, r)
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d", w.Code)
		}
	}
	if strings.Contains(output.String(), secret) {
		t.Fatal("untrusted CSP field leaked into log")
	}
	if !strings.Contains(output.String(), "script-src-elem") || !strings.Contains(output.String(), "unknown") {
		t.Fatal("safe diagnostic fields missing")
	}
}
