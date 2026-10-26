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

// TestSignatureInputLimits verifies that handleEntrySignatureAdd rejects oversized
// signature_name (>100 chars) and signature_at (>50 chars) prior to processing or DB updates.
func TestSignatureInputLimits(t *testing.T) {
	s := &Server{
		cfg: &config.Config{SessionSecret: "test-session-secret-at-least-16-bytes"},
	}

	for _, tc := range []struct {
		name, field, message string
		limit                int
	}{
		{
			name:    "signature_name over limit",
			field:   "signature_name",
			message: fmt.Sprintf("Name darf höchstens %d Zeichen lang sein.", maxNameLen),
			limit:   maxNameLen,
		},
		{
			name:    "signature_at over limit",
			field:   "signature_at",
			message: "Unterschriftszeitpunkt darf höchstens 50 Zeichen lang sein.",
			limit:   50,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{
				"signature_name": {"Max Mustermann"},
				"signature_at":   {"2026-03-30T12:00:00Z"},
			}
			form.Set(tc.field, strings.Repeat("a", tc.limit+1))

			assertFormRedirect(
				t,
				s,
				s.handleEntrySignatureAdd,
				form,
				"/entries/1/edit#unterschrift",
				tc.message,
			)
		})
	}
}

// TestSignatureInputLimitsAllowBoundary proves that ordinary values and values
// exactly at each ceiling pass validation and proceed to store lookups.
func TestSignatureInputLimitsAllowBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, sigName, sigAt string
	}{
		{name: "ordinary", sigName: "Hans Huber", sigAt: "2026-03-30T12:00:00Z"},
		{name: "name at limit", sigName: strings.Repeat("a", maxNameLen), sigAt: "2026-03-30T12:00:00Z"},
		{name: "at limit", sigName: "Hans Huber", sigAt: "2026-03-30T12:00:00Z" + strings.Repeat(" ", 30)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, queries := quickServer(t)
			form := url.Values{
				"signature_name": {tc.sigName},
				"signature_at":   {tc.sigAt},
			}
			req := httptest.NewRequest(http.MethodPost, "/entries/1/signature", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.SetPathValue("id", "1")
			rr := httptest.NewRecorder()

			s.handleEntrySignatureAdd(rr, req)

			if *queries != 1 {
				t.Errorf("database calls = %d, want 1 entry lookup", *queries)
			}
		})
	}
}
