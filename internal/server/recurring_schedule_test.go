package server

import (
	"errors"
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
		wantErr     error
	}{
		{name: "unbounded", wantNil: true},
		{name: "inclusive", value: "2026-10-01"},
		{name: "malformed", value: "01.10.2026", wantErr: errInvalidRecurringEnd},
		{name: "before next", value: "2026-09-30", wantErr: errRecurringEndBeforeRun},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/recurring/1/update", strings.NewReader(url.Values{"ends_on": {tc.value}}.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			_ = req.ParseForm()
			got, err := optionalRecurringEnd(req, next)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || (got == nil) != tc.wantNil {
				t.Fatalf("end=%v err=%v", got, err)
			}
		})
	}
}

// TestRecurringStartDateNormalizesFallback verifies an invalid form value does
// not retain the current time of day and invalidate an inclusive end date.
func TestRecurringStartDateNormalizesFallback(t *testing.T) {
	now := time.Date(2026, time.September, 30, 23, 59, 58, 123, time.FixedZone("CEST", 2*60*60))
	got := recurringStartDate("invalid", now)
	want := time.Date(2026, time.October, 7, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("fallback = %v, want %v", got, want)
	}
}
