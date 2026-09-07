package metrics

import (
	"strings"
	"testing"
)

func render() string {
	var b strings.Builder
	Render(&b)
	return b.String()
}

func TestCounterAndLabelRendering(t *testing.T) {
	Inc("treckrr_test_plain_total")
	Add("treckrr_test_plain_total", 4)
	IncLabel("treckrr_test_labeled_total", "class", "5xx")
	IncLabel("treckrr_test_labeled_total", "class", "2xx")
	IncLabel("treckrr_test_labeled_total", "class", "2xx")

	out := render()
	for _, want := range []string{
		"# TYPE treckrr_test_plain_total counter",
		"treckrr_test_plain_total 5",
		`treckrr_test_labeled_total{class="5xx"} 1`,
		`treckrr_test_labeled_total{class="2xx"} 2`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("exposition missing %q\n%s", want, out)
		}
	}
	// A labeled family must carry exactly one HELP/TYPE header, or the
	// Prometheus text parser rejects the whole scrape.
	if n := strings.Count(out, "# TYPE treckrr_test_labeled_total counter"); n != 1 {
		t.Errorf("labeled family has %d TYPE headers, want exactly 1", n)
	}
}

// The histogram must be cumulative — bucket le=0.1 counts everything at or below
// 0.1, not just what fell between 0.025 and 0.1 — and _count must equal +Inf.
func TestHistogramIsCumulative(t *testing.T) {
	for _, d := range []float64{0.001, 0.05, 0.05, 3, 30} {
		ObserveRequest(d)
	}
	out := render()
	var line5ms, line100ms, lineInf, lineCount string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, `treckrr_http_request_duration_seconds_bucket{le="0.005"}`):
			line5ms = l
		case strings.HasPrefix(l, `treckrr_http_request_duration_seconds_bucket{le="0.1"}`):
			line100ms = l
		case strings.HasPrefix(l, `treckrr_http_request_duration_seconds_bucket{le="+Inf"}`):
			lineInf = l
		case strings.HasPrefix(l, "treckrr_http_request_duration_seconds_count"):
			lineCount = l
		}
	}
	if line5ms == "" || line100ms == "" || lineInf == "" || lineCount == "" {
		t.Fatalf("histogram lines missing:\n%s", out)
	}
	f := func(l string) string { return l[strings.LastIndex(l, " ")+1:] }
	if f(line5ms) != "1" {
		t.Errorf("le=0.005 has %s, want 1", f(line5ms))
	}
	if f(line100ms) != "3" { // 0.001 + 0.05 + 0.05
		t.Errorf("le=0.1 has %s, want 3 (cumulative)", f(line100ms))
	}
	if f(lineInf) != "5" {
		t.Errorf("le=+Inf has %s, want 5", f(lineInf))
	}
	if f(lineCount) != f(lineInf) {
		t.Errorf("_count %s != +Inf bucket %s", f(lineCount), f(lineInf))
	}
}

func TestConcurrentIncIsRaceFree(t *testing.T) {
	const n = 200
	done := make(chan struct{})
	for range 4 {
		go func() {
			for range n {
				Inc("treckrr_test_concurrent_total")
				ObserveRequest(0.01)
			}
			done <- struct{}{}
		}()
	}
	for range 4 {
		<-done
	}
	if !strings.Contains(render(), "treckrr_test_concurrent_total 800") {
		t.Error("concurrent increments lost counts")
	}
}
