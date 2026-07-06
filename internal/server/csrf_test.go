package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"treckrr/internal/config"
)

func testServer() *Server {
	return &Server{cfg: &config.Config{SessionSecret: "test-secret-at-least-16"}}
}

func TestInjectCSRFField(t *testing.T) {
	html := []byte(`<form method="post" action="/x"><input name="a"></form>` +
		`<form method="get" action="/y"></form>` +
		"<form\n  method=\"post\"\n  action=\"/z\">\n</form>")
	out := string(injectCSRFField(html, "TOKEN123"))

	if got := strings.Count(out, `name="csrf_token" value="TOKEN123"`); got != 2 {
		t.Fatalf("expected token injected into 2 POST forms, got %d\n%s", got, out)
	}
	// The GET form must not receive a token.
	getPart := out[strings.Index(out, `action="/y"`):]
	if strings.Contains(getPart[:strings.Index(getPart, "</form>")], "csrf_token") {
		t.Fatalf("GET form should not get a CSRF token:\n%s", out)
	}
}

func TestInjectCSRFField_NoTokenNoChange(t *testing.T) {
	html := []byte(`<form method="post"></form>`)
	if got := string(injectCSRFField(html, "")); got != string(html) {
		t.Fatalf("empty token must not modify HTML, got %q", got)
	}
}

func TestCSRFMiddleware(t *testing.T) {
	s := testServer()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := s.csrf(ok)

	sessionValue := "sess-abc"
	token := func() string {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
		return s.csrfToken(r)
	}()

	newPost := func(withSession bool, field string) *http.Request {
		body := url.Values{}
		if field != "" {
			body.Set(csrfFieldName, field)
		}
		r := httptest.NewRequest(http.MethodPost, "/entries", strings.NewReader(body.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if withSession {
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
		}
		return r
	}

	cases := []struct {
		name string
		req  *http.Request
		want int
	}{
		{"no session passes (pre-login POST)", newPost(false, ""), http.StatusOK},
		{"session + valid token passes", newPost(true, token), http.StatusOK},
		{"session + missing token rejected", newPost(true, ""), http.StatusForbidden},
		{"session + wrong token rejected", newPost(true, "nope"), http.StatusForbidden},
		{"safe GET passes", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: sessionValue})
			return r
		}(), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, tc.req)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
		})
	}
}
