package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBadRequestRendersBrandedPage pins that s.badRequest emits the branded HTML
// error page with a 400 and the given message (and a default when empty), rather
// than net/http's plain-text default.
func TestBadRequestRendersBrandedPage(t *testing.T) {
	s := &Server{}

	rr := httptest.NewRecorder()
	s.badRequest(rr, "Testmeldung")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("content-type=%q, want text/html", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Testmeldung") {
		t.Errorf("body is missing the message")
	}
	if !strings.Contains(body, "Ungültige Anfrage") {
		t.Errorf("body is missing the branded title")
	}

	// An empty message falls back to a default.
	rr2 := httptest.NewRecorder()
	s.badRequest(rr2, "")
	if !strings.Contains(rr2.Body.String(), "ungültig") {
		t.Errorf("empty message should use the default text")
	}
}
