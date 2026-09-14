package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestHandleMannstundenAndAnfahrtAddValidation(t *testing.T) {
	s := testPaymentServer(t)

	t.Run("overly long entry_date in Mannstunden rejected", func(t *testing.T) {
		longDate := strings.Repeat("2026-01-01", 5) + "X" // 51 chars
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("entry_date", longDate)
		form.Set("hours", "5")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/mannstunden", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleMannstundenAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Datum darf höchstens 50 Zeichen lang sein.") {
			t.Errorf("expected long date flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long hours in Mannstunden rejected", func(t *testing.T) {
		longHours := strings.Repeat("9", maxDecimalLen+1)
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("entry_date", "2026-03-30")
		form.Set("hours", longHours)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/mannstunden", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleMannstundenAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Stunden darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long hours flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long hourly_rate in Mannstunden rejected", func(t *testing.T) {
		longRate := strings.Repeat("9", maxDecimalLen+1)
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("entry_date", "2026-03-30")
		form.Set("hours", "5")
		form.Set("hourly_rate", longRate)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/mannstunden", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleMannstundenAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Stundensatz darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long hourly rate flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long entry_date in Anfahrt rejected", func(t *testing.T) {
		longDate := strings.Repeat("2026-01-01", 5) + "X" // 51 chars
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("entry_date", longDate)
		form.Set("km", "10")

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/anfahrt", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleAnfahrtAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Datum darf höchstens 50 Zeichen lang sein.") {
			t.Errorf("expected long date flash message, got cookie: %q", flashCookie)
		}
	})

	t.Run("overly long km in Anfahrt rejected", func(t *testing.T) {
		longKm := strings.Repeat("9", maxDecimalLen+1)
		form := url.Values{}
		form.Set("year_id", "1")
		form.Set("entry_date", "2026-03-30")
		form.Set("km", longKm)

		req := httptest.NewRequest(http.MethodPost, "/neighbors/1/anfahrt", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("id", "1")
		rr := httptest.NewRecorder()

		s.handleAnfahrtAdd(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Errorf("expected status SeeOther, got %v", rr.Code)
		}
		flashCookie := flashText(t, s, rr)
		if !strings.Contains(flashCookie, "Kilometer darf höchstens 32 Zeichen lang sein.") {
			t.Errorf("expected long km flash message, got cookie: %q", flashCookie)
		}
	})
}
