package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/models"
)

func TestSecurityPolicyReadFailureStopsRequests(t *testing.T) {
	for _, cached := range []bool{false, true} {
		s := testServer() // no policy store, including an expired permissive cache
		s.policy.ok, s.policy.at = cached, time.Now().Add(-policyCacheTTL-time.Second)
		if _, err := s.securityPolicy(t.Context()); err == nil {
			t.Fatal("failed policy refresh returned a usable policy")
		}
		for _, wrap := range []func(http.HandlerFunc) http.Handler{s.auth, s.admin} {
			reached := false
			r := httptest.NewRequest(http.MethodGet, "/account/calendar", nil)
			u := &models.User{ID: 1, Role: models.RoleAdmin, IsAdmin: true}
			r = r.WithContext(context.WithValue(r.Context(), userCacheKey, &userCache{user: u, done: true}))
			w := httptest.NewRecorder()
			wrap(func(http.ResponseWriter, *http.Request) { reached = true }).ServeHTTP(w, r)
			if reached || w.Code != http.StatusInternalServerError {
				t.Fatalf("policy read failure reached handler=%v status=%d", reached, w.Code)
			}
		}
	}
}

func TestSecurityPolicyRejectsMalformedGrace(t *testing.T) {
	for _, raw := range []string{"", "oops", "-1", "91", "1.5", strings.Repeat("9", 30)} {
		r := httptest.NewRequest(http.MethodPost, "/admin/security-policy", strings.NewReader(url.Values{"grace_days": {raw}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		testServer().handleSecurityPolicySave(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("grace %q was not rejected before storage: %d", raw, w.Code)
		}
	}
}

func TestMFAEnrollmentAndViewerExceptions(t *testing.T) {
	for _, p := range []string{"/profile", "/account/2fa", "/account/2fa/qr.png", "/account/passkeys/register/finish", "/account/password"} {
		if !isMFASetupPath(p) {
			t.Errorf("enrollment blocked: %s", p)
		}
	}
	for _, p := range []string{"/account/calendar", "/account/calendar/delete", "/account/favorites", "/account/notifications", "/notifications/1/open", "/profile-export"} {
		if isMFASetupPath(p) {
			t.Errorf("MFA bypass: %s", p)
		}
	}
	for _, p := range []string{"/notifications/read-all", "/notifications/1/open", "/notifications/2/dismiss"} {
		if !isSelfServicePath(p) {
			t.Errorf("personal notification blocked: %s", p)
		}
	}
	for _, p := range []string{"/notifications/1/delete", "/notifications/no/open", "/entries", "/accounting", "/profiles"} {
		if isSelfServicePath(p) {
			t.Errorf("viewer mutation bypass: %s", p)
		}
	}
}
