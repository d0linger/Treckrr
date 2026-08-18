package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMaintenanceGate pins the [T-05] restore gate: while maintenance is on, a
// normal route gets 503 with Retry-After and the wrapped handler never runs; the
// health probes and static assets stay reachable; and with maintenance off every
// path passes through untouched.
func TestMaintenanceGate(t *testing.T) {
	s := &Server{}
	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	gate := s.maintenanceGate(next)

	call := func(path string) (int, bool) {
		reached = false
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, reached
	}

	// --- maintenance OFF: everything passes through ---
	if code, ok := call("/neighbors/1"); code != http.StatusOK || !ok {
		t.Fatalf("off: /neighbors/1 = %d reached=%v, want 200 true", code, ok)
	}

	// --- maintenance ON ---
	s.setMaintenance(true)

	if code, ok := call("/neighbors/1"); code != http.StatusServiceUnavailable || ok {
		t.Errorf("on: /neighbors/1 = %d reached=%v, want 503 false", code, ok)
	}
	if code, ok := call("/"); code != http.StatusServiceUnavailable || ok {
		t.Errorf("on: / = %d reached=%v, want 503 false", code, ok)
	}
	// Health probes stay up so the orchestrator doesn't restart the app mid-restore.
	for _, p := range []string{"/livez", "/readyz", "/healthz"} {
		if code, ok := call(p); code != http.StatusOK || !ok {
			t.Errorf("on: %s = %d reached=%v, want 200 true (probe must stay up)", p, code, ok)
		}
	}
	// Static assets stay up so the 503 page's CSS renders.
	if code, ok := call("/static/css/app.css"); code != http.StatusOK || !ok {
		t.Errorf("on: /static/... = %d reached=%v, want 200 true", code, ok)
	}
	// The 503 carries a Retry-After hint.
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/neighbors/1", nil))
	if rec.Header().Get("Retry-After") == "" {
		t.Error("503 should set Retry-After")
	}

	// --- back OFF: passes through again ---
	s.setMaintenance(false)
	if code, ok := call("/neighbors/1"); code != http.StatusOK || !ok {
		t.Errorf("off again: /neighbors/1 = %d reached=%v, want 200 true", code, ok)
	}
}
