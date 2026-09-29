package server

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestOptionalRecurringEnd validates empty, malformed and reversed form dates.
func TestOptionalRecurringEnd(t *testing.T) {
	next := time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, value string
		wantNil     bool
		wantErr     string
	}{
		{name: "unbounded", wantNil: true},
		{name: "inclusive", value: "2026-10-01"},
		{name: "malformed", value: "01.10.2026", wantErr: "gültiges Enddatum"},
		{name: "before next", value: "2026-09-30", wantErr: "nicht vor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/recurring/1/update", strings.NewReader(url.Values{"ends_on": {tc.value}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_ = req.ParseForm()
			got, err := optionalRecurringEnd(req, next)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || (got == nil) != tc.wantNil {
				t.Fatalf("end=%v err=%v", got, err)
			}
		})
	}
}
