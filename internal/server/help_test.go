package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/web"
)

// TestHelpPageAndLinks renders the public glossary without a session and
// proves that every inline help link in the templates points at an entry.
func TestHelpPageAndLinks(t *testing.T) {
	t.Parallel()
	tpl, err := web.Templates()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{cfg: &config.Config{SessionSecret: "test-secret-at-least-16"}, templates: tpl}
	w := httptest.NewRecorder()
	s.handleHelp(w, httptest.NewRequest("GET", "/hilfe", nil))
	page := w.Body.String()
	if w.Code != 200 || strings.Contains(page, "drawer__nav") {
		t.Fatalf("anonymous glossary: status %d, chrome shown=%v", w.Code, strings.Contains(page, "drawer__nav"))
	}
	ids := map[string]bool{}
	for _, term := range glossary {
		if ids[term.ID] {
			t.Errorf("duplicate glossary id %q", term.ID)
		}
		ids[term.ID] = true
		if !strings.Contains(page, `id="`+term.ID+`"`) || !strings.Contains(page, term.Term) {
			t.Errorf("glossary page lacks %q", term.ID)
		}
	}
	refs := regexp.MustCompile(`help-link" "([a-z-]+)"|/hilfe#([a-z-]+)`)
	files, err := filepath.Glob(filepath.Join("..", "web", "templates", "*.html"))
	if err != nil || len(files) == 0 {
		t.Fatalf("templates not found: %v", err)
	}
	used := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range refs.FindAllStringSubmatch(string(raw), -1) {
			id := m[1] + m[2]
			used++
			if !ids[id] {
				t.Errorf("%s links to unknown glossary entry %q", filepath.Base(file), id)
			}
		}
	}
	if used < 6 {
		t.Errorf("only %d help links found in templates", used)
	}
}
