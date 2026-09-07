// Package metrics is a dependency-free counter/histogram registry for the
// Prometheus exposition served at /metrics.
//
// It lives in its own package because two very different callers feed it: the
// HTTP middleware in internal/server, and the maintenance loop in cmd/treckrr,
// which has no Server to hang state off. Everything is process-local and
// lock-free on the hot path (atomics), because the alternative — a metrics
// library — would be the app's first heavyweight dependency for what amounts to
// twenty counters.
//
// Counters only ever increase and are never reset; a scrape reads the current
// value. That is exactly what Prometheus expects, and it means a restart shows
// up as a counter reset rather than as lost data.
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Counter names. Kept as constants so a typo is a compile error rather than a
// silently separate series.
const (
	HTTPRequests        = "treckrr_http_requests_total"
	HTTPRequestsByClass = "treckrr_http_responses_total" // labeled 2xx/3xx/4xx/5xx
	HTTPPanics          = "treckrr_http_panics_total"

	LoginFailed    = "treckrr_login_failed_total"
	LoginBlocked   = "treckrr_login_blocked_total"
	CSRFRejected   = "treckrr_csrf_rejected_total"
	RateLimitTrips = "treckrr_rate_limit_trips_total"

	RecurringCreated = "treckrr_recurring_bookings_created_total"
	AuditPurged      = "treckrr_audit_rows_purged_total"
	MaintenanceRuns  = "treckrr_maintenance_runs_total"
	MaintenanceFails = "treckrr_maintenance_failures_total"
	MailSent         = "treckrr_mail_sent_total"
	MailFailed       = "treckrr_mail_failed_total"
)

var (
	mu       sync.RWMutex
	counters = map[string]*atomic.Int64{}

	// Request duration in seconds. Fixed buckets, cumulative at render time —
	// a full histogram type would be more than this needs.
	histMu      sync.Mutex
	histBuckets = []float64{0.005, 0.025, 0.1, 0.5, 1, 2.5, 5, 10}
	histCounts  = make([]int64, len(histBuckets)+1) // last is +Inf
	histSum     float64
)

func counter(name string) *atomic.Int64 {
	mu.RLock()
	c, ok := counters[name]
	mu.RUnlock()
	if ok {
		return c
	}
	mu.Lock()
	defer mu.Unlock()
	if c, ok := counters[name]; ok { // another goroutine won the race
		return c
	}
	c = &atomic.Int64{}
	counters[name] = c
	return c
}

// Inc adds one to a counter, creating it on first use.
func Inc(name string) { counter(name).Add(1) }

// Add adds n to a counter (n may be zero, which still registers the series so a
// quiet instance exposes an explicit 0 rather than nothing).
func Add(name string, n int64) { counter(name).Add(n) }

// IncLabel increments a single-label series, e.g. IncLabel(HTTPResponses,
// "class", "5xx"). The label pair is folded into the key and split again at
// render time.
func IncLabel(name, label, value string) {
	Inc(name + "\x00" + label + "\x00" + value)
}

// ObserveRequest records one request's duration for the latency histogram.
func ObserveRequest(seconds float64) {
	histMu.Lock()
	defer histMu.Unlock()
	histSum += seconds
	for i, b := range histBuckets {
		if seconds <= b {
			histCounts[i]++
			return
		}
	}
	histCounts[len(histBuckets)]++
}

// Render writes the Prometheus text exposition for everything recorded so far.
func Render(b *strings.Builder) {
	mu.RLock()
	names := make([]string, 0, len(counters))
	for n := range counters {
		names = append(names, n)
	}
	vals := make(map[string]int64, len(counters))
	for n, c := range counters {
		vals[n] = c.Load()
	}
	mu.RUnlock()
	sort.Strings(names)

	// Group labeled series under one HELP/TYPE header, as the format requires.
	seenHeader := map[string]bool{}
	for _, n := range names {
		base, label, value := splitKey(n)
		if !seenHeader[base] {
			fmt.Fprintf(b, "# HELP %s Count of %s.\n# TYPE %s counter\n", base, strings.TrimSuffix(strings.TrimPrefix(base, "treckrr_"), "_total"), base)
			seenHeader[base] = true
		}
		if label == "" {
			fmt.Fprintf(b, "%s %d\n", base, vals[n])
			continue
		}
		fmt.Fprintf(b, "%s{%s=%q} %d\n", base, label, value, vals[n])
	}

	histMu.Lock()
	counts := append([]int64(nil), histCounts...)
	sum := histSum
	histMu.Unlock()

	fmt.Fprintf(b, "# HELP treckrr_http_request_duration_seconds Request duration.\n# TYPE treckrr_http_request_duration_seconds histogram\n")
	var cum int64
	for i, ub := range histBuckets {
		cum += counts[i]
		fmt.Fprintf(b, "treckrr_http_request_duration_seconds_bucket{le=\"%g\"} %d\n", ub, cum)
	}
	cum += counts[len(histBuckets)]
	fmt.Fprintf(b, "treckrr_http_request_duration_seconds_bucket{le=\"+Inf\"} %d\n", cum)
	fmt.Fprintf(b, "treckrr_http_request_duration_seconds_sum %g\n", sum)
	fmt.Fprintf(b, "treckrr_http_request_duration_seconds_count %d\n", cum)
}

func splitKey(k string) (base, label, value string) {
	parts := strings.Split(k, "\x00")
	if len(parts) == 3 {
		return parts[0], parts[1], parts[2]
	}
	return k, "", ""
}
