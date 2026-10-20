package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestMonitoringDocsUseRealMetrics keeps docs/grafana and docs/prometheus in
// step with the code: the dashboard is valid JSON, and every treckrr_* series
// either file queries is emitted somewhere in the application source.
func TestMonitoringDocsUseRealMetrics(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..")
	dashboard, err := os.ReadFile(filepath.Join(root, "docs", "grafana", "treckrr.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(dashboard, &parsed); err != nil {
		t.Fatalf("dashboard is not valid JSON: %v", err)
	}
	alerts, err := os.ReadFile(filepath.Join(root, "docs", "prometheus", "treckrr-alerts.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var source strings.Builder
	err = filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		source.Write(raw)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	code := source.String()
	names := regexp.MustCompile(`treckrr_[a-z0-9_]+`)
	seen := map[string]bool{}
	for _, name := range names.FindAllString(string(dashboard)+string(alerts), -1) {
		base := name
		for _, suffix := range []string{"_bucket", "_sum", "_count"} {
			base = strings.TrimSuffix(base, suffix)
		}
		if seen[base] {
			continue
		}
		seen[base] = true
		if !strings.Contains(code, `"`+base) {
			t.Errorf("monitoring docs query %q, which the application never emits", name)
		}
	}
	if len(seen) < 10 {
		t.Fatalf("only %d metric names referenced; did the docs move?", len(seen))
	}
}
