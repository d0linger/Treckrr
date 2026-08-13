package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"
)

// handleMetrics serves a minimal Prometheus text-format exposition (no external
// dependency): process/runtime gauges plus the SQL connection-pool stats. It is
// only wired up when METRICS_TOKEN is set, and requires that token as a bearer
// credential so a scraper — but not an anonymous caller — can read it.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !s.metricsAuthorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	db := s.store.DBStats()

	var b strings.Builder
	gauge := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", name, help, name, name, v)
	}
	counter := func(name, help string, v float64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %g\n", name, help, name, name, v)
	}

	fmt.Fprintf(&b, "# HELP treckrr_build_info Build information.\n# TYPE treckrr_build_info gauge\ntreckrr_build_info{go_version=%q} 1\n", runtime.Version())
	gauge("treckrr_uptime_seconds", "Seconds since process start.", time.Since(s.started).Seconds())

	gauge("go_goroutines", "Number of goroutines that currently exist.", float64(runtime.NumGoroutine()))
	gauge("go_memstats_alloc_bytes", "Bytes of allocated heap objects still in use.", float64(mem.Alloc))
	gauge("go_memstats_heap_inuse_bytes", "Bytes in in-use heap spans.", float64(mem.HeapInuse))
	gauge("go_memstats_sys_bytes", "Bytes of memory obtained from the OS.", float64(mem.Sys))
	counter("go_memstats_gc_total", "Number of completed GC cycles.", float64(mem.NumGC))

	gauge("treckrr_db_connections_open", "Open connections to the database (in use + idle).", float64(db.OpenConnections))
	gauge("treckrr_db_connections_in_use", "Connections currently in use.", float64(db.InUse))
	gauge("treckrr_db_connections_idle", "Idle connections in the pool.", float64(db.Idle))
	gauge("treckrr_db_connections_max_open", "Maximum allowed open connections (0 = unlimited).", float64(db.MaxOpenConnections))
	counter("treckrr_db_wait_total", "Total number of connection waits due to pool exhaustion.", float64(db.WaitCount))
	counter("treckrr_db_wait_seconds_total", "Total time blocked waiting for a connection.", db.WaitDuration.Seconds())

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(b.String()))
}

// metricsAuthorized does a constant-time comparison of the bearer token against
// METRICS_TOKEN. Both sides are hashed first so the compare doesn't leak the
// token length via an early length mismatch.
func (s *Server) metricsAuthorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := sha256.Sum256([]byte(strings.TrimPrefix(h, prefix)))
	want := sha256.Sum256([]byte(s.cfg.MetricsToken))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}
