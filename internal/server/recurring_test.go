package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRecurringNextRunLengthLimit(t *testing.T) {
	t.Parallel()
	// A nil store proves rejection happens before any database access, without
	// coupling this field check to the entry query's column layout.
	s := testServer()
	for name, handler := range map[string]http.HandlerFunc{
		"create": s.handleRecurringCreate,
		"update": s.handleRecurringUpdate,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name, input string
			}{
				{name: "over boundary", input: strings.Repeat("x", maxNameLen+1)},
				{name: "padded date", input: strings.Repeat(" ", maxNameLen) + "2099-02-03"},
				{name: "unicode padding", input: strings.Repeat("\u2003", maxNameLen) + "2099-02-03"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					form := url.Values{"next_run": {tc.input}}
					req := httptest.NewRequest(http.MethodPost, "/recurring", strings.NewReader(form.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.SetPathValue("id", "1")
					rr := httptest.NewRecorder()
					handler(rr, req)
					if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/recurring" {
						t.Fatalf("expected redirect to /recurring, got %d %q", rr.Code, rr.Header().Get("Location"))
					}
					if got := flashText(t, s, rr); !strings.Contains(got, "Startdatum darf höchstens 100 Zeichen lang sein.") {
						t.Fatalf("unexpected flash: %q", got)
					}
				})
			}
		})
	}
}
