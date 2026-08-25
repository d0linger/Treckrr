package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// flashText decodes the flash message a handler set on the recorder. The flash
// cookie is HMAC-signed and base64-encoded (signFlash), so a test cannot grep the
// raw Set-Cookie header for the German text — it has to go through the same
// verification path the browser round-trip would.
func flashText(t *testing.T, s *Server, rr *httptest.ResponseRecorder) string {
	t.Helper()
	for _, raw := range rr.Header().Values("Set-Cookie") {
		c, err := http.ParseSetCookie(raw)
		if err != nil || c.Value == "" {
			continue
		}
		if c.Name != flashCookie && c.Name != hostCookiePrefix+flashCookie {
			continue
		}
		payload, ok := s.verifyFlash(c.Value)
		if !ok {
			t.Fatalf("flash cookie failed signature verification: %q", raw)
		}
		parts := strings.SplitN(payload, "|", 4)
		if len(parts) < 3 {
			t.Fatalf("malformed flash payload: %q", payload)
		}
		msg, err := url.QueryUnescape(parts[2])
		if err != nil {
			t.Fatalf("flash message is not valid query-escaping: %q", parts[2])
		}
		return msg
	}
	return ""
}

// A flash cookie is rendered into the page AND can arm a POST form that
// injectCSRFField stamps a valid CSRF token into, so only this server may mint
// one. Anything an attacker could plant must be dropped.
func TestReadFlashRejectsForgedAndExpiredCookies(t *testing.T) {
	s := testServer()

	valid := func(exp time.Time) string {
		return s.signFlash(strconv.FormatInt(exp.Unix(), 10) + "|error|" +
			url.QueryEscape("Echte Meldung") + "|/entries/1/delete")
	}

	cases := []struct {
		name  string
		value string
	}{
		{"unsigned legacy payload", "error|" + url.QueryEscape("Bitte hier neu anmelden") + "|/years/1/delete"},
		{"attacker-authored, no signature", "1799999999|error|Angriff|/entries/1/delete"},
		{"tampered signature", strings.TrimSuffix(valid(time.Now().Add(time.Minute)), "a") + "b"},
		{"tampered payload, original signature", func() string {
			enc, sig, _ := strings.Cut(valid(time.Now().Add(time.Minute)), ".")
			return enc[:len(enc)-1] + "X." + sig
		}()},
		{"signed but expired", valid(time.Now().Add(-time.Second))},
		{"oversized", strings.Repeat("A", maxFlashCookieLen+1)},
		{"not base64", "!!!.abc"},
		{"no separator", "abcdef"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: flashCookie, Value: tc.value})
			msg, kind, undo := s.readFlash(httptest.NewRecorder(), r)
			if msg != "" || kind != "" || undo != "" {
				t.Errorf("rejected cookie still produced a flash: msg=%q kind=%q undo=%q", msg, kind, undo)
			}
		})
	}
}

// The round trip must still work, undo target included — the signature is a gate,
// not a feature removal.
func TestReadFlashAcceptsOwnCookie(t *testing.T) {
	s := testServer()

	setRR := httptest.NewRecorder()
	setReq := httptest.NewRequest(http.MethodPost, "/payments/1/delete", nil)
	s.setFlashUndo(setRR, setReq, "success", "Zahlung gelöscht.", "/payments/1/restore")

	c, err := http.ParseSetCookie(setRR.Header().Get("Set-Cookie"))
	if err != nil {
		t.Fatalf("parse Set-Cookie: %v", err)
	}
	readReq := httptest.NewRequest(http.MethodGet, "/", nil)
	readReq.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})

	msg, kind, undo := s.readFlash(httptest.NewRecorder(), readReq)
	if msg != "Zahlung gelöscht." || kind != "success" || undo != "/payments/1/restore" {
		t.Errorf("round trip lost data: msg=%q kind=%q undo=%q", msg, kind, undo)
	}
}

// Even a validly-signed flash may not point its undo POST off-origin.
func TestReadFlashRejectsOffOriginUndoTarget(t *testing.T) {
	s := testServer()
	exp := strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)

	for _, target := range []string{"//evil.example", `/\evil.example`, "https://evil.example/x", "entries/1/delete"} {
		t.Run(target, func(t *testing.T) {
			value := s.signFlash(exp + "|info|" + url.QueryEscape("Hinweis") + "|" + url.QueryEscape(target))
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.AddCookie(&http.Cookie{Name: flashCookie, Value: value})
			msg, _, undo := s.readFlash(httptest.NewRecorder(), r)
			if msg != "Hinweis" {
				t.Fatalf("message should survive, got %q", msg)
			}
			if undo != "" {
				t.Errorf("off-origin undo target was honored: %q", undo)
			}
		})
	}
}
