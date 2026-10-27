package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
	"github.com/d0linger/treckrr/internal/store"
)

// TestResolveNonPositiveIDs verifies that resolveYear and resolveBase reject
// non-positive numeric IDs (<= 0) upfront and fall back safely without executing
// store Get calls on non-positive IDs.
func TestResolveNonPositiveIDs(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "zero", raw: "0"},
		{name: "negative", raw: "-1"},
		{name: "large negative", raw: "-99999"},
	} {
		t.Run("year "+tc.name, func(t *testing.T) {
			s := &Server{}
			// With a nil store, if resolveYear attempted s.store.GetBillingYear,
			// it would panic. Non-positive IDs skip GetBillingYear and reach
			// LatestBillingYear (which panics on s.store.LatestBillingYear here,
			// proving GetBillingYear was never called with the invalid ID).
			r := httptest.NewRequest(http.MethodGet, "/?year="+tc.raw, nil)
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic on LatestBillingYear due to nil store")
				}
				errStr := ""
				if e, ok := r.(error); ok {
					errStr = e.Error()
				}
				if strings.Contains(errStr, "GetBillingYear") {
					t.Errorf("GetBillingYear was called for non-positive ID %s", tc.raw)
				}
			}()
			s.resolveYear(httptest.NewRecorder(), r)
		})

		t.Run("base "+tc.name, func(t *testing.T) {
			s := &Server{}
			r := httptest.NewRequest(http.MethodGet, "/?base="+tc.raw, nil)
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic on LatestBase due to nil store")
				}
				errStr := ""
				if e, ok := r.(error); ok {
					errStr = e.Error()
				}
				if strings.Contains(errStr, "GetBase") {
					t.Errorf("GetBase was called for non-positive ID %s", tc.raw)
				}
			}()
			s.resolveBase(httptest.NewRecorder(), r)
		})
	}
}

// TestCalendarMonthAndPrecheckQuerySanitization tests query parameter length
// sanitization on month and entry_date.
func TestCalendarMonthAndPrecheckQuerySanitization(t *testing.T) {
	t.Parallel()

	t.Run("calendarMonth oversized input", func(t *testing.T) {
		oversized := strings.Repeat("2026-05", 100)
		r := httptest.NewRequest(http.MethodGet, "/?month="+url.QueryEscape(oversized), nil)
		year := &models.BillingYear{Year: 2026}
		now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)

		got := calendarMonth(r, year, store.BookingFilter{}, now)
		expected := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
		if !got.Equal(expected) {
			t.Errorf("got %v, want fallback to %v", got, expected)
		}
	})

	t.Run("calendarMonth valid input", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/?month=2026-08", nil)
		year := &models.BillingYear{Year: 2026}
		now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)

		got := calendarMonth(r, year, store.BookingFilter{}, now)
		expected := time.Date(2026, 8, 1, 0, 0, 0, 0, time.Local)
		if !got.Equal(expected) {
			t.Errorf("got %v, want %v", got, expected)
		}
	})

	t.Run("handleEntryPrecheck oversized entry_date", func(t *testing.T) {
		s := &Server{}
		oversized := strings.Repeat("2026-05-01", 100)
		r := httptest.NewRequest(http.MethodGet, "/precheck?entry_date="+url.QueryEscape(oversized)+"&hours=2", nil)
		w := httptest.NewRecorder()

		s.handleEntryPrecheck(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
		if !strings.Contains(w.Body.String(), `{"warn":""}`) {
			t.Errorf("body = %q, want empty warn", w.Body.String())
		}
	})
}
