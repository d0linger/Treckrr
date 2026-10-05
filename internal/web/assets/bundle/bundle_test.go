package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGeneratedAssetsCurrent prevents source-layer changes from bypassing the
// embedded CSS and JavaScript delivered by the server.
func TestGeneratedAssetsCurrent(t *testing.T) {
	t.Parallel()
	if err := Verify(".."); err != nil {
		t.Fatal(err)
	}
}

func TestYearSelectorInheritsThemeColorScheme(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile(filepath.Join("..", "css", "30-components.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(body)
	start := strings.Index(css, ".yearselect select {")
	if start < 0 {
		t.Fatal("year selector rule not found")
	}
	end := strings.Index(css[start:], "}")
	if end < 0 {
		t.Fatal("year selector rule is incomplete")
	}
	if rule := css[start : start+end]; strings.Contains(rule, "color-scheme") {
		t.Errorf("year selector must inherit the active root color scheme: %s", rule)
	}
}
