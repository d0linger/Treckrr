package server

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLimitBody pins the request-body cap to the middleware itself: a body of
// exactly maxRequestBody bytes reads cleanly, one byte more fails with
// *http.MaxBytesError. Asserting on the read (rather than a downstream handler's
// status) keeps the test on limitBody's own contract — it swaps r.Body and writes
// no status of its own. TestHandlerBodyLimit is the companion that verifies
// Server.Handler() actually installs the limiter in the chain.
func TestLimitBody(t *testing.T) {
	s := &Server{}

	// The wrapped handler drains the body and records what it saw.
	var readN int
	var readErr error
	drain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		readN, readErr = len(b), err
	})
	h := s.limitBody(drain)

	post := func(n int) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/entries", bytes.NewReader(bytes.Repeat([]byte("a"), n)))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}

	t.Run("exactly at the limit reads fully", func(t *testing.T) {
		readN, readErr = 0, nil
		h.ServeHTTP(httptest.NewRecorder(), post(maxRequestBody))
		if readErr != nil {
			t.Fatalf("unexpected read error at the limit: %v", readErr)
		}
		if readN != maxRequestBody {
			t.Fatalf("read %d bytes, want %d", readN, maxRequestBody)
		}
	})

	t.Run("one byte over the limit is rejected", func(t *testing.T) {
		readN, readErr = 0, nil
		h.ServeHTTP(httptest.NewRecorder(), post(maxRequestBody+1))
		var mbe *http.MaxBytesError
		if !errors.As(readErr, &mbe) {
			t.Fatalf("expected *http.MaxBytesError, got %v", readErr)
		}
	})
}

// TestHandlerBodyLimit is the wiring companion to TestLimitBody: it drives a real
// request through Server.Handler() to prove the limiter is actually installed in
// the chain, ahead of form parsing. An oversized POST /login must return 400 —
// handleLogin's ParseForm hits the MaxBytesReader read error before it reaches the
// rate limiter or store — whereas without the middleware the body would parse and
// the request would proceed. No DB is needed: the request carries no session
// cookie, so csrf/accessLog/auth never touch the (nil) store.
func TestHandlerBodyLimit(t *testing.T) {
	s := testServer()
	h := s.Handler()

	body := bytes.NewReader(bytes.Repeat([]byte("a"), maxRequestBody+1))
	req := httptest.NewRequest(http.MethodPost, "/login", body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized POST /login status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}
