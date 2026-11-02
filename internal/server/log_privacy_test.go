package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode"

	"github.com/d0linger/treckrr/internal/config"
)

func TestShareTokenLogRedaction(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	s := &Server{cfg: &config.Config{}}
	token := strings.Repeat("a", 64)
	for _, path := range []string{"/s/beleg/" + token, "/s/beleg/" + token + "/extra",
		"/s/portal/" + token + "/invoice.pdf", "/s/konto/" + token, "/s/konto/" + token + "/feedback", "/cal/" + token + ".ics"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		s.accessLog(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(httptest.NewRecorder(), r)
	}
	if strings.Contains(output.String(), token) {
		t.Fatal("bearer token leaked into access log")
	}
	for _, want := range []string{"/s/beleg/[redacted]", "/s/portal/[redacted]/invoice.pdf", "/s/konto/[redacted]/feedback", "/cal/[redacted]"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("redacted route %s missing", want)
		}
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

// TestAuditLogSanitizesErrors checks both audit-write failure branches on
// their own. The quickServer mock fails every statement, so each call must log
// exactly one failure record; decoding the JSON compares the values as logged,
// not the handler's escaped text.
func TestAuditLogSanitizesErrors(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	s, _ := quickServer(t)
	r := httptest.NewRequest(http.MethodPost, "/test", nil)
	for _, tc := range []struct {
		name string
		call func()
		want map[string]string
	}{
		{name: "audit", call: func() { s.audit(r, "action\r\ninjected", "entity\r\ninjected", 1, "detail") },
			want: map[string]string{"action": "action  injected", "entity": "entity  injected"}},
		{name: "auditLogin", call: func() { s.auditLogin(r, "user", "action\r\ninjected", "detail") },
			want: map[string]string{"action": "action  injected"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output.Reset()
			tc.call()
			var failures []map[string]any
			for dec := json.NewDecoder(&output); dec.More(); {
				var record map[string]any
				if err := dec.Decode(&record); err != nil {
					t.Fatal(err)
				}
				if record["msg"] == "audit write failed" {
					failures = append(failures, record)
				}
			}
			if len(failures) != 1 {
				t.Fatalf("%d audit-write failure records, want 1", len(failures))
			}
			for key, want := range tc.want {
				if got := failures[0][key]; got != want {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
			if msg, _ := failures[0]["err"].(string); msg == "" || strings.IndexFunc(msg, unicode.IsControl) >= 0 {
				t.Errorf("err = %q, want a non-empty message without control characters", msg)
			}
		})
	}
}
