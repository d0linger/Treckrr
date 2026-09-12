package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNotFoundUsesBrandedPageForHTMLNavigation checks that only HTML GET requests
// receive branded 404s while other clients retain plain responses and no-store.
func TestNotFoundUsesBrandedPageForHTMLNavigation(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		accept      string
		wantType    string
		wantContent string
	}{
		{
			name:        "browser navigation",
			method:      http.MethodGet,
			accept:      "text/html,application/xhtml+xml",
			wantType:    "text/html",
			wantContent: "Seite nicht gefunden",
		},
		{
			name:        "image client",
			method:      http.MethodGet,
			accept:      "image/avif,image/webp,*/*",
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
		{
			name:        "API client",
			method:      http.MethodGet,
			accept:      "application/json",
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
		{
			name:        "client without Accept",
			method:      http.MethodGet,
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
		{
			name:        "form submission keeps plain response",
			method:      http.MethodPost,
			accept:      "text/html",
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
		{
			name:        "HEAD keeps existing response type",
			method:      http.MethodHead,
			accept:      "text/html",
			wantType:    "text/plain",
			wantContent: "404 page not found",
		},
	}

	s := &Server{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, "/missing", nil)
			req.Header.Set("Accept", tt.accept)
			rr.Header().Set("Cache-Control", "no-store")

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
			if rr.Header().Get("Cache-Control") != "no-store" {
				t.Error("error response lost the authentication middleware's no-store policy")
			}
		})
	}
}

// TestInvalidRecordIDsPreserveResponseContracts checks representative handlers
// for branded page recovery and compact non-page 404s without accessing a store.
func TestInvalidRecordIDsPreserveResponseContracts(t *testing.T) {
	t.Parallel()
	s := &Server{}
	for _, tc := range []struct {
		name     string
		method   string
		accept   string
		path     string
		handler  http.HandlerFunc
		wantType string
	}{
		{
			name: "neighbor page", method: http.MethodGet, accept: "text/html",
			path: "/neighbors/invalid", handler: s.handleNeighborDetail, wantType: "text/html",
		},
		{
			name: "neighbor update", method: http.MethodPost, accept: "text/html",
			path: "/neighbors/invalid/update", handler: s.handleNeighborUpdate, wantType: "text/plain",
		},
		{
			name: "booking deletion", method: http.MethodPost, accept: "text/html",
			path: "/entries/invalid/delete", handler: s.handleEntryDelete, wantType: "text/plain",
		},
		{
			name: "photo request", method: http.MethodGet, accept: "image/avif,image/webp,*/*",
			path: "/entries/invalid/photos/1", handler: s.handleEntryPhotoServe, wantType: "text/plain",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Accept", tc.accept)
			req.SetPathValue("id", "invalid")
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rr.Code)
			}
			if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want %q", got, tc.wantType)
			}
			if tc.wantType == "text/plain" {
				if got := rr.Body.String(); got != "404 page not found\n" {
					t.Errorf("non-page response = %q, want compact 404", got)
				}
				return
			}
			if !strings.Contains(rr.Body.String(), `href="/">Zur Übersicht</a>`) {
				t.Error("browser error page is missing its recovery link")
			}
		})
	}
}
