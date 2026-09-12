package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotFoundUsesBrandedPageForHTMLNavigation(t *testing.T) {
	tests := []struct {
		name        string
		accept      string
		wantType    string
		wantContent string
	}{
		{
			name:        "browser navigation",
			accept:      "text/html,application/xhtml+xml",
			wantType:    "text/html",
			wantContent: "Seite nicht gefunden",
		},
		{
			name:        "image client",
			accept:      "image/avif,image/webp,*/*",
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
		{
			name:        "API client",
			accept:      "application/json",
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
	}

	s := &Server{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/missing", nil)
			req.Header.Set("Accept", tt.accept)

			s.notFound(rr, req)

			if rr.Code != http.StatusNotFound {
				t.Fatalf("status=%d, want 404", rr.Code)
			}
			if contentType := rr.Header().Get("Content-Type"); !strings.Contains(contentType, tt.wantType) {
				t.Errorf("content-type=%q, want %q", contentType, tt.wantType)
			}
			if body := rr.Body.String(); !strings.Contains(body, tt.wantContent) {
				t.Errorf("body is missing %q", tt.wantContent)
			}
		})
	}
}
