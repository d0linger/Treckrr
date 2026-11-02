package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/models"
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

// TestInvoiceCreditInputLimitPreservesSelection rejects oversized input before
// database access without losing the document selected by the form.
func TestInvoiceCreditInputLimitPreservesSelection(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	assertFormRedirect(
		t,
		s,
		s.handleInvoiceGutschrift,
		url.Values{
			"year_id": {"1"}, "invoice_id": {"42"},
			"amount": {strings.Repeat("1", maxDecimalLen+1)},
		},
		"/neighbors/1/beleg?year=1&invoice_id=42",
		fmt.Sprintf("Betrag darf höchstens %d Zeichen lang sein.", maxDecimalLen),
	)
}

// TestInvoiceCreditInvalidSelection rejects malformed IDs even when another
// validation error would otherwise redirect before any document lookup.
func TestInvoiceCreditInvalidSelection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{name: "empty", ids: []string{""}},
		{name: "malformed", ids: []string{"broken"}},
		{name: "duplicate", ids: []string{"42", "43"}},
		{name: "overflow", ids: []string{"9223372036854775808"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			form := url.Values{
				"year_id": {"1"}, "invoice_id": tc.ids,
				"amount": {strings.Repeat("1", maxDecimalLen+1)},
			}
			req := httptest.NewRequest(http.MethodPost, "/validation", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("id", "1")
			rr := httptest.NewRecorder()
			s.handleInvoiceGutschrift(rr, req)
			if rr.Code != http.StatusBadRequest || rr.Header().Get("Location") != "" {
				t.Fatalf("response = %d %q, want 400 without redirect", rr.Code, rr.Header().Get("Location"))
			}
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

// TestFuelAdjustmentInputLimits rejects oversized dates and amounts before the
// base-lock lookup, preserving the return location and field-specific message.
func TestFuelAdjustmentInputLimits(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, field, value, message string
	}{
		{
			name: "date over limit", field: "effective_from",
			value:   "2026-10-15" + strings.Repeat(" ", 41),
			message: "Stichtag darf höchstens 50 Zeichen lang sein.",
		},
		{
			name: "amount over limit", field: "amount_per_h",
			value:   strings.Repeat("1", maxDecimalLen+1),
			message: "Zuschlag darf höchstens 32 Zeichen lang sein.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
			form := url.Values{
				"base_id": {"7"}, "label": {"Dieselzuschlag"},
				"effective_from": {"2026-10-15"}, "amount_per_h": {"1,50"},
			}
			form.Set(tc.field, tc.value)
			assertFormRedirect(
				t,
				s,
				s.handleFuelAdjustmentSave,
				form,
				pricesURL(7)+"#fuel-adjustments",
				tc.message,
			)
		})
	}
}

// TestFuelAdjustmentInputLimitsAllowBoundary proves that ordinary values and
// values exactly at each ceiling still reach the existing base lookup. The
// counting mock deliberately does not serve a base or perform any writes.
func TestFuelAdjustmentInputLimitsAllowBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, date, amount string
	}{
		{name: "ordinary", date: "2026-10-15", amount: "1,50"},
		{name: "date at limit", date: "2026-10-15" + strings.Repeat(" ", 40), amount: "1,50"},
		{name: "amount at limit", date: "2026-10-15", amount: "1," + strings.Repeat("0", maxDecimalLen-2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, queries := quickServer(t)
			form := url.Values{
				"base_id": {"7"}, "label": {"Dieselzuschlag"},
				"effective_from": {tc.date}, "amount_per_h": {tc.amount},
			}
			req := httptest.NewRequest(http.MethodPost, "/prices/fuel-adjustments", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()

			s.handleFuelAdjustmentSave(rr, req)

			if *queries != 1 {
				t.Errorf("database calls = %d, want exactly the base lookup", *queries)
			}
			if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "Unbekannte Bemessungsgrundlage") {
				t.Errorf("response = %d %q, want unavailable-base response", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestLogin2FACodeLimit uses a valid pending token so the code guard, not an
// expired cookie or rate limiter, must reject before accessing the absent store.
func TestLogin2FACodeLimit(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	pending := s.signPending2FA(123, []byte("binding"))
	form := url.Values{"totp": {strings.Repeat("1", maxNameLen+1)}, csrfFieldName: {s.pending2FACSRFToken(pending)}}
	req := httptest.NewRequest(http.MethodPost, "/login/2fa", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: pending2FACookie, Value: pending})
	rr := httptest.NewRecorder()
	s.handleLogin2FA(rr, req)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("response = %d %q, want redirect to login", rr.Code, rr.Header().Get("Location"))
	}
	if got := flashText(t, s, rr); got != "Code darf höchstens 100 Zeichen lang sein." {
		t.Fatalf("flash = %q", got)
	}
}

// TestRecurringIntervalLimits rejects oversized cadence values in every
// recurring-rule creation and update path before accessing the database.
func TestRecurringIntervalLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "booking create", handler: s.handleRecurringCreate},
		{name: "booking update", handler: s.handleRecurringUpdate},
		{name: "ledger create", handler: s.handleLedgerRecurringCreate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"next_run":      {"2026-09-28"},
				"interval_kind": {strings.Repeat("w", maxNameLen+1)},
			}
			assertFormRedirect(
				t,
				s,
				tc.handler,
				form,
				"/recurring",
				"Intervall darf höchstens 100 Zeichen lang sein.",
			)
		})
	}
}

// TestPasskeyRegisterBeginPasswordLimit rejects impossible bcrypt passwords
// before consuming a persistent verification-attempt allowance.
func TestPasskeyRegisterBeginPasswordLimit(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name     string
		password string
	}{
		{name: "ASCII", password: strings.Repeat("a", 73)},
		{name: "UTF-8", password: strings.Repeat("ä", 37)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"password":%q}`, tc.password)
			req := httptest.NewRequest(http.MethodPost, "/profile/passkeys/register/begin", strings.NewReader(body))
			rr := httptest.NewRecorder()

			s.handlePasskeyRegisterBegin(rr, req)

			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
			}
			if got := strings.TrimSpace(rr.Body.String()); got != "Passwort darf höchstens 72 Byte lang sein." {
				t.Errorf("body = %q", got)
			}
		})
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
		{name: "2FA confirm", field: "password", back: twoFactorRetryPath, handler: s.handleTwoFactorConfirm},
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
			twoFactorRetryPath,
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
					neighborPaymentsURL(1, 1),
					message,
				)
			})
		}
	}
}

// TestSavedViewQueryLimit rejects oversized filters before parsing or store access.
func TestSavedViewQueryLimit(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	form := url.Values{
		"name":  {"Meine Ansicht"},
		"query": {strings.Repeat("a", maxSavedViewQueryLen+1)},
	}
	req := httptest.NewRequest(http.MethodPost, "/views/bookings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{ID: 1, Username: "testuser"}))
	rr := httptest.NewRecorder()

	s.handleSavedViewCreate(rr, req)

	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/buchungen" {
		t.Fatalf("response = %d %q, want redirect to /buchungen", rr.Code, rr.Header().Get("Location"))
	}
	if got := flashText(t, s, rr); !strings.Contains(got, "Filter darf höchstens 1024 Zeichen lang sein.") {
		t.Fatalf("flash = %q, want over-limit message", got)
	}
}

// TestSecurityPolicyGraceDaysInputLimit rejects oversized grace_days before
// parsing or accessing security policy storage.
func TestSecurityPolicyGraceDaysInputLimit(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	form := url.Values{
		"grace_days": {strings.Repeat("1", 51)},
	}
	assertFormRedirect(
		t,
		s,
		s.handleSecurityPolicySave,
		form,
		"/admin/users#anmelderichtlinie",
		"Übergangsfrist darf höchstens 50 Zeichen lang sein.",
	)
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

// TestFieldInputLimits rejects oversized field attributes before neighbor lookups.
func TestFieldInputLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name, field, label string
		limit              int
	}{
		{name: "field name", field: "name", label: "Feldname", limit: maxNameLen},
		{name: "plot number", field: "plot_number", label: "Schlagnummer", limit: maxPlotNumberLen},
		{name: "area", field: "area_ha", label: "Fläche", limit: maxDecimalLen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"year": {"1"}, "name": {"Feld"}, tc.field: {strings.Repeat("1", tc.limit+1)}}
			assertFormRedirect(
				t,
				s,
				s.handleFieldCreate,
				form,
				"/neighbors/1?year=1#felder",
				fmt.Sprintf("%s darf höchstens %d Zeichen lang sein.", tc.label, tc.limit),
			)
		})
	}
}

func TestMutationHandlersRejectMalformedForms(t *testing.T) {
	// A valid header token bypasses the CSRF middleware's form parser. Each
	// handler must still reject an unreadable body before any store access.
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "reset TOTP", handler: s.handleUserResetTotp},
		{name: "deactivate user", handler: s.handleUserDelete},
		{name: "delete accounting profile", handler: s.handleAccountingProfileDelete},
		{name: "add signature", handler: s.handleEntrySignatureAdd},
		{name: "create field", handler: s.handleFieldCreate},
		{name: "archive field", handler: s.handleFieldArchive},
		{name: "delete field", handler: s.handleFieldDelete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, body := range []struct {
				name, contentType, data string
			}{
				{name: "urlencoded", contentType: "application/x-www-form-urlencoded", data: "data=%XX"},
				{name: "multipart without boundary", contentType: "multipart/form-data", data: "unreadable"},
				{
					name: "truncated multipart", contentType: "multipart/form-data; boundary=test-boundary",
					data: "--test-boundary\r\nContent-Disposition: form-data; name=\"data\"\r\n\r\nvalue",
				},
			} {
				t.Run(body.name, func(t *testing.T) {
					r := httptest.NewRequest(http.MethodPost, "/validation", strings.NewReader(body.data))
					r.Header.Set("Content-Type", body.contentType)
					r.SetPathValue("id", "1")
					r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "mutation-validation-session"})
					r.Header.Set(csrfHeaderName, s.csrfToken(r))
					w := httptest.NewRecorder()
					s.csrf(tc.handler).ServeHTTP(w, r)
					if w.Code != http.StatusBadRequest {
						t.Fatalf("status = %d, want 400", w.Code)
					}
				})
			}
		})
	}
}

func TestInvoiceHandlersRejectInvalidYearIDs(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name, field string
		handler     http.HandlerFunc
	}{
		{name: "confirm", field: "year", handler: s.handleInvoiceConfirm},
		{name: "issue", field: "year_id", handler: s.handleInvoiceIssue},
		{name: "storno", field: "year_id", handler: s.handleInvoiceStorno},
		{name: "credit", field: "year_id", handler: s.handleInvoiceGutschrift},
	} {
		for _, raw := range []string{"0", "-1", "9223372036854775808", "-9223372036854775809", "broken"} {
			t.Run(tc.name+"/"+raw, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodPost, "/validation", strings.NewReader(url.Values{tc.field: {raw}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.SetPathValue("id", "1")
				w := httptest.NewRecorder()
				tc.handler(w, r)
				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400", w.Code)
				}
			})
		}
	}
}

// TestPathIDRejectsNonPositive accepts positive path IDs and refuses zero and
// negative ones with errNonPositiveID, for {id} and for a named value.
func TestPathIDRejectsNonPositive(t *testing.T) {
	t.Parallel()
	for _, val := range []string{"0", "-1", "-100"} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.SetPathValue("id", val)
		req.SetPathValue("pid", val)
		if id, err := pathID(req); !errors.Is(err, errNonPositiveID) {
			t.Errorf("pathID(%q) = %d, %v; want errNonPositiveID", val, id, err)
		}
		if id, err := formInt64FromPath(req, "pid"); !errors.Is(err, errNonPositiveID) {
			t.Errorf("formInt64FromPath(%q) = %d, %v; want errNonPositiveID", val, id, err)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetPathValue("id", "42")
	req.SetPathValue("pid", "7")
	if id, err := pathID(req); err != nil || id != 42 {
		t.Errorf("pathID(\"42\") = %d, %v; want 42, nil", id, err)
	}
	if id, err := formInt64FromPath(req, "pid"); err != nil || id != 7 {
		t.Errorf("formInt64FromPath(\"7\") = %d, %v; want 7, nil", id, err)
	}
	req.SetPathValue("id", "abc")
	if _, err := pathID(req); err == nil || errors.Is(err, errNonPositiveID) {
		t.Errorf("pathID(\"abc\") = %v; want a parse error", err)
	}
}

// TestBillingRuleInputLimits rejects an oversized increment or minimum with
// the length message before parsing or touching the store.
func TestBillingRuleInputLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		field, label string
	}{
		{field: "billing_increment_h", label: "Abrechnungstakt"},
		{field: "minimum_h", label: "Mindestzeit"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			form := url.Values{tc.field: {strings.Repeat("1", maxDecimalLen+1)}}
			assertFormRedirect(t, s, s.handleBaseBillingRule, form, "/bases#grundlage-1",
				tc.label+" darf höchstens 32 Zeichen lang sein.")
		})
	}
}

// TestSignatureRejectsBeforeLookup refuses a missing or oversized signature
// field before the entry lookup, with the same messages the booking form shows.
func TestSignatureRejectsBeforeLookup(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	pad := "data:image/png;base64,AAAA"
	for _, tc := range []struct {
		name, message string
		form          url.Values
	}{
		{name: "name too long", message: "Bitte den Namen der unterschreibenden Person angeben (höchstens 100 Zeichen).",
			form: url.Values{"signature_name": {strings.Repeat("a", maxNameLen+1)}, "signature_png": {pad}}},
		{name: "image without name", message: "Bitte den Namen der unterschreibenden Person angeben",
			form: url.Values{"signature_png": {pad}}},
		{name: "name without image", message: "Bitte vor Ort unterschreiben lassen",
			form: url.Values{"signature_name": {"Hans Huber"}}},
		{name: "empty", message: "Bitte unterschreiben lassen und den Namen angeben.", form: url.Values{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFormRedirect(t, s, s.handleEntrySignatureAdd, tc.form, "/entries/1/edit#unterschrift", tc.message)
		})
	}
}

// TestSignatureAtLimitReachesLookup lets a name of exactly maxNameLen
// characters (not bytes) through to the single entry lookup.
func TestSignatureAtLimitReachesLookup(t *testing.T) {
	s, queries := quickServer(t)
	form := url.Values{"signature_name": {strings.Repeat("ä", maxNameLen)}, "signature_png": {"data:image/png;base64,AAAA"}}
	req := httptest.NewRequest(http.MethodPost, "/entries/1/signature", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("id", "1")
	s.handleEntrySignatureAdd(httptest.NewRecorder(), req)
	if *queries != 1 {
		t.Errorf("database calls = %d, want the one entry lookup", *queries)
	}
}

func TestYearHandlersRejectNonPositiveIDs(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, tc := range []struct {
		name, back string
		form       url.Values
		handler    http.HandlerFunc
	}{
		{name: "create", back: "/years", form: url.Values{"year": {"2026"}, "base_id": {"-1"}}, handler: s.handleYearCreate},
		{name: "update", back: "/years", form: url.Values{"base_id": {"0"}}, handler: s.handleYearUpdate},
		{name: "add", back: dashboardURL(1), form: url.Values{"year_id": {"1"}, "neighbor_id": {"-5"}}, handler: s.handleYearAddNeighbor},
		{name: "remove", back: dashboardURL(-1), form: url.Values{"year_id": {"-1"}, "neighbor_id": {"1"}}, handler: s.handleYearRemoveNeighbor},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := "Bitte einen Nachbarn wählen."
			if tc.back == "/years" {
				message = "Bitte eine Bemessungsgrundlage wählen."
			}
			assertFormRedirect(t, s, tc.handler, tc.form, tc.back, message)
		})
	}
}

func TestAccountingProfileColumnLimits(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"}}
	for _, format := range []string{"custom", "bmd", "rzl"} {
		for _, tc := range []struct {
			name, message string
			columns       []string
		}{
			{name: "count", columns: make([]string, maxFormListLen+1), message: "Zu viele Exportspalten ausgewählt."},
			{name: "length", columns: []string{strings.Repeat("x", maxNoteLen+1)}, message: fmt.Sprintf("Exportspalten darf höchstens %d Zeichen lang sein.", maxNoteLen)},
		} {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				assertFormRedirect(t, s, s.handleAccountingProfileSave,
					url.Values{"name": {"Profile"}, "format": {format}, "columns": tc.columns}, "/rechnungsjournal", tc.message)
			})
		}
	}
}
