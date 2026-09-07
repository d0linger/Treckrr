package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoverPanicTurnsPanicInto500(t *testing.T) {
	s := &Server{}
	h := s.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("kaboom")
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil)) // must not propagate
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestRecoverPanicPassesCleanRequestsThrough(t *testing.T) {
	s := &Server{}
	h := s.recoverPanic(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))
	if rr.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418 untouched", rr.Code)
	}
}

// net/http uses this sentinel to abort a response on purpose (e.g. when a
// client disconnects mid-stream); swallowing it would break that contract, so
// it must keep traveling.
func TestRecoverPanicReRaisesErrAbortHandler(t *testing.T) {
	s := &Server{}
	h := s.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if recover() != http.ErrAbortHandler { //nolint:errorlint // sentinel per net/http contract
			t.Error("ErrAbortHandler was swallowed instead of re-raised")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	t.Error("unreachable: the panic should have propagated")
}
