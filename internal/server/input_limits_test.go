package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
)

// TestInvoiceInputLimits rejects oversized values before any database work.
func TestInvoiceInputLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name, field, label, back string
		limit                    int
		handler                  http.HandlerFunc
	}{
		{name: "invoice date", field: "issued_on", label: "Rechnungsdatum", back: "/neighbors/1/beleg?year=1", limit: 50, handler: s.handleInvoiceIssue},
		{name: "credit amount", field: "amount", label: "Betrag", back: "/neighbors/1/beleg?year=1&rechnung=1", limit: maxDecimalLen, handler: s.handleInvoiceGutschrift},
		{name: "advance due date", field: "due_on", label: "Fällig am", back: "/neighbors/1/beleg?year=1", limit: 50, handler: s.handleAnzahlungCreate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"year_id": {"1"}, tc.field: {strings.Repeat("1", tc.limit+1)}}
			assertFormRedirect(
				t,
				s,
				tc.handler,
				form,
				tc.back,
				fmt.Sprintf("%s darf höchstens %d Zeichen lang sein.", tc.label, tc.limit),
			)
		})
	}
}

// TestPriceDecimalLimits prevents oversized rates from silently becoming zero.
func TestPriceDecimalLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		field, label string
		handler      http.HandlerFunc
	}{
		{field: "cost_per_ps", label: "Kosten je PS", handler: s.handleLoadLevelSave},
		{field: "ps", label: "PS", handler: s.handleTractorSave},
		{field: "working_width", label: "Arbeitsbreite", handler: s.handleMachineSave},
		{field: "cost_per_ab", label: "Kosten", handler: s.handleMachineSave},
		{field: "self_cost_per_h", label: "Selbstkosten", handler: s.handleMachineSave},
	} {
		t.Run(tc.field, func(t *testing.T) {
			form := url.Values{"base_id": {"1"}, tc.field: {strings.Repeat("1", maxDecimalLen+1)}}
			assertFormRedirect(
				t,
				s,
				tc.handler,
				form,
				pricesURL(1),
				tc.label+" darf höchstens 32 Zeichen lang sein.",
			)
		})
	}
}

// TestLogin2FACodeLimit uses a valid pending token so the code guard, not an
// expired cookie or rate limiter, must reject before accessing the absent store.
func TestLogin2FACodeLimit(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	form := url.Values{"totp": {strings.Repeat("1", maxNameLen+1)}}
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: pending2FACookie, Value: s.signPending2FA(123)})
	rr := httptest.NewRecorder()
	s.handleLogin2FA(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("response = %d %q, want redirect to login", rr.Code, rr.Header().Get("Location"))
	}
	if got := flashText(t, s, rr); got != "Code darf höchstens 100 Zeichen lang sein." {
		t.Fatalf("flash = %q", got)
	}
}

// TestStepUpInputLimits checks that oversized credentials never reach the
// database or consume an admission attempt, including multibyte passwords.
func TestStepUpInputLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, route := range []struct {
		name, field, back string
		handler           http.HandlerFunc
	}{
		{name: "password change", field: "current_password", back: "/account/password", handler: s.handleAccountPasswordSubmit},
		{name: "2FA confirm", field: "password", back: "/account/2fa", handler: s.handleTwoFactorConfirm},
		{name: "recovery codes", field: "password", back: "/account/2fa", handler: s.handleRecoveryRegenerate},
		{name: "2FA disable", field: "password", back: "/account/2fa", handler: s.handleTwoFactorDisable},
	} {
		for _, value := range []struct{ name, password string }{
			{name: "ASCII", password: strings.Repeat("a", 73)},
			{name: "UTF-8", password: strings.Repeat("ä", 37)},
		} {
			t.Run(route.name+"/"+value.name, func(t *testing.T) {
				form := url.Values{route.field: {value.password}, "code": {"123456"}}
				assertFormRedirect(
					t,
					s,
					route.handler,
					form,
					route.back,
					"Passwort darf höchstens 72 Byte lang sein.",
				)
			})
		}
	}
	t.Run("2FA code", func(t *testing.T) {
		form := url.Values{"password": {"test"}, "code": {strings.Repeat("1", 101)}}
		assertFormRedirect(
			t,
			s,
			s.handleTwoFactorConfirm,
			form,
			"/account/2fa",
			"Code darf höchstens 100 Zeichen lang sein.",
		)
	})
}

// TestPasswordByteLimit pins both sides of bcrypt's byte boundary without
// imposing a new complexity policy on an existing password.
func TestPasswordByteLimit(t *testing.T) {
	s := testAccountServer(t)
	for _, tc := range []struct {
		name, password string
		rejected       bool
	}{
		{name: "empty"},
		{name: "ASCII at limit", password: strings.Repeat("a", 72)},
		{name: "UTF-8 at limit", password: strings.Repeat("ä", 36)},
		{name: "ASCII over limit", password: strings.Repeat("a", 73), rejected: true},
		{name: "UTF-8 over limit", password: strings.Repeat("ä", 37), rejected: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/account/password", nil)
			rr := httptest.NewRecorder()
			if got := s.passwordTooLong(rr, req, tc.password); got != tc.rejected {
				t.Errorf("rejected = %v, want %v", got, tc.rejected)
			}
		})
	}
}

// TestCompanyDecimalLimits rejects each oversized setting before accessing
// saved company data, rather than silently saving the parser's zero fallback.
func TestCompanyDecimalLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct{ field, label string }{
		{field: "dunning_fee_1", label: "Mahnspesen 1. Stufe"},
		{field: "dunning_fee_2", label: "Mahnspesen 2. Stufe"},
		{field: "vat_rate", label: "USt-Satz"},
		{field: "travel_flat", label: "Anfahrt pauschal"},
		{field: "travel_per_km", label: "Anfahrt je km"},
		{field: "small_business_limit", label: "Kleinunternehmergrenze"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			form := url.Values{tc.field: {strings.Repeat("1", 33)}}
			assertFormRedirect(
				t,
				s,
				s.handleCompanySave,
				form,
				"/admin/company",
				tc.label+" darf höchstens 32 Zeichen lang sein.",
			)
		})
	}
}

// TestLegacyBookingInputLimits covers the labor and travel endpoints still
// used by legacy forms, before person/company lookups or booking writes.
func TestLegacyBookingInputLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name, field, message string
		limit                int
		handler              http.HandlerFunc
	}{
		{name: "labor date", field: "entry_date", message: "Datum darf höchstens 50 Zeichen lang sein.", limit: 50, handler: s.handleMannstundenAdd},
		{name: "labor hours", field: "hours", message: "Stunden darf höchstens 32 Zeichen lang sein.", limit: 32, handler: s.handleMannstundenAdd},
		{name: "labor rate", field: "hourly_rate", message: "Stundensatz darf höchstens 32 Zeichen lang sein.", limit: 32, handler: s.handleMannstundenAdd},
		{name: "travel date", field: "entry_date", message: "Datum darf höchstens 50 Zeichen lang sein.", limit: 50, handler: s.handleAnfahrtAdd},
		{name: "travel distance", field: "km", message: "Kilometer darf höchstens 32 Zeichen lang sein.", limit: 32, handler: s.handleAnfahrtAdd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"year_id": {"1"}, "person_id": {"1"}, "entry_date": {"2026-03-30"}, "hours": {"5"}, "hourly_rate": {"10"}, "km": {"10"}}
			form.Set(tc.field, strings.Repeat("1", tc.limit+1))
			assertFormRedirect(
				t,
				s,
				tc.handler,
				form,
				neighborURL(1, 1),
				tc.message,
			)
		})
	}
}

// TestRecurringInputLimits checks that oversized recurring input parameters are rejected.
func TestRecurringInputLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name, field, label string
		handler            http.HandlerFunc
	}{
		{name: "create next_run", field: "next_run", label: "Startdatum", handler: s.handleRecurringCreate},
		{name: "create interval_kind", field: "interval_kind", label: "Intervall", handler: s.handleRecurringCreate},
		{name: "update next_run", field: "next_run", label: "Startdatum", handler: s.handleRecurringUpdate},
		{name: "update interval_kind", field: "interval_kind", label: "Intervall", handler: s.handleRecurringUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"next_run": {"2026-03-30"}, "interval_kind": {"weekly"}}
			form.Set(tc.field, strings.Repeat("a", maxNameLen+1))
			assertFormRedirect(
				t,
				s,
				tc.handler,
				form,
				"/recurring",
				tc.label+" darf höchstens 100 Zeichen lang sein.",
			)
		})
	}
}

// TestPaymentDateLimits checks rejection at 51 characters and preserves both
// ordinary dates and padded dates at the existing 50-character boundary.
func TestPaymentDateLimits(t *testing.T) {
	s := testPaymentServer(t)
	for _, route := range []struct {
		name, field, success string
		handler              http.HandlerFunc
	}{
		{name: "payment update", field: "paid_on", success: "Zahlung aktualisiert.", handler: s.handlePaymentUpdate},
		{name: "installment", field: "due_on", success: "Rate hinzugefügt.", handler: s.handleInstallmentAdd},
	} {
		for _, tc := range []struct{ name, date string }{
			{name: "normal", date: "2026-03-30"},
			{name: "at limit", date: "2026-03-30" + strings.Repeat(" ", 40)},
			{name: "over limit", date: "2026-03-30" + strings.Repeat(" ", 41)},
		} {
			t.Run(route.name+"/"+tc.name, func(t *testing.T) {
				form := url.Values{"year_id": {"1"}, "amount": {"100.00"}, route.field: {tc.date}}
				message := route.success
				if len(tc.date) > 50 {
					message = "Datum darf höchstens 50 Zeichen lang sein."
				}
				assertFormRedirect(
					t,
					s,
					route.handler,
					form,
					neighborURL(1, 1),
					message,
				)
			})
		}
	}
}

// assertFormRedirect checks the handler's complete redirect/flash response.
// Rejection-only callers deliberately omit a store: any database use panics.
func assertFormRedirect(
	t *testing.T,
	s *Server,
	handler http.HandlerFunc,
	form url.Values,
	back string,
	message string,
) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/validation", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if got := rr.Header().Get("Location"); got != back {
		t.Errorf("redirect = %q, want %q", got, back)
	}
	if got := flashText(t, s, rr); !strings.Contains(got, message) {
		t.Errorf("flash = %q, want %q", got, message)
	}
}
