package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/d0linger/treckrr/internal/config"
)

// Compile-time proof that every ResponseWriter wrapper in the chain keeps
// http.ResponseController's Unwrap path intact. Dropping either method compiles
// fine on its own but silently disables SetWriteDeadline for the handlers below.
var (
	_ interface{ Unwrap() http.ResponseWriter } = (*statusRecorder)(nil)
	_ interface{ Unwrap() http.ResponseWriter } = (*gzipResponseWriter)(nil)
)

// TestExtendWriteDeadlineThroughMiddleware pins the regression that made every
// backup deadline a no-op: accessLog wraps each request in a statusRecorder, and
// while that wrapper had no Unwrap(), http.ResponseController could not reach the
// underlying *http.response. SetWriteDeadline then returned ErrNotSupported into a
// discarded error, and the server's short global WriteTimeout stayed in force —
// cutting off long dumps and restores after the work was already done.
//
// It needs a real server: httptest.NewRecorder cannot set a deadline either, so
// recording the handler alone would pass no matter what.
func TestExtendWriteDeadlineThroughMiddleware(t *testing.T) {
	s := &Server{cfg: &config.Config{SessionSecret: "test-secret-at-least-16"}}

	var deadlineErr error
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		deadlineErr = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
		_, _ = w.Write([]byte("ok"))
	})

	// Same wrapping order the real chain uses: accessLog installs the recorder.
	srv := httptest.NewServer(s.userCacheMW(s.accessLog(inner)))
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/deadline-probe")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}

	if deadlineErr != nil {
		t.Fatalf("SetWriteDeadline through the middleware chain failed: %v "+
			"(a ResponseWriter wrapper is missing Unwrap; the global WriteTimeout "+
			"would silently apply to backup dumps, downloads and restores)", deadlineErr)
	}
}
