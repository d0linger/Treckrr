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
// status) keeps the test on limitBody's own contract — it swaps r.Body and
// writes no status of its own — and guards against the guard silently dropping
// out if the middleware chain is ever reordered.
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
