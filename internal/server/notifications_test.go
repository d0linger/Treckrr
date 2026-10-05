package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/models"
)

func TestHandleNotificationPreferencesWithoutSMTP(t *testing.T) {
	tests := []struct {
		name        string
		weekly      bool
		wantMessage string
	}{
		{
			name:        "opt out remains available",
			wantMessage: "Benachrichtigungseinstellungen gespeichert.",
		},
		{
			name:        "opt in stays blocked",
			weekly:      true,
			wantMessage: "Der Wochenbericht kann erst aktiviert werden, wenn SMTP konfiguriert ist.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testAdminServer(t)
			form := url.Values{}
			if tt.weekly {
				form.Set("weekly_email", "on")
			}
			req := httptest.NewRequest(
				http.MethodPost,
				"/account/notifications",
				strings.NewReader(form.Encode()),
			)
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req = req.WithContext(context.WithValue(req.Context(), userCtxKey, &models.User{
				ID:    7,
				Email: "user@example.test",
			}))
			rr := httptest.NewRecorder()

			s.handleNotificationPreferences(rr, req)

			if rr.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
			}
			if got := flashText(t, s, rr); got != tt.wantMessage {
				t.Errorf("flash = %q, want %q", got, tt.wantMessage)
			}
		})
	}
}
