package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/config"
	"github.com/d0linger/treckrr/internal/web"
)

// TestDemoModeBannerOnEveryPage renders the anonymous login page, which bypasses
// newPage, to prove the practice-instance banner and tab prefix come from
// render itself and appear only when DEMO_MODE is on.
func TestDemoModeBannerOnEveryPage(t *testing.T) {
	t.Parallel()
	tpl, err := web.Templates()
	if err != nil {
		t.Fatal(err)
	}
	for _, demo := range []bool{false, true} {
		s := &Server{cfg: &config.Config{SessionSecret: "test-secret-at-least-16", DemoMode: demo}, templates: tpl}
		w := httptest.NewRecorder()
		s.render(w, httptest.NewRequest("GET", "/login", nil), "login", pageData{"Title": "Anmelden"})
		body := w.Body.String()
		hasBanner := strings.Contains(body, "data-demo-banner") && strings.Contains(body, "Übungsinstanz")
		hasPrefix := strings.Contains(body, "<title>Übung · Anmelden · Treckrr</title>")
		if hasBanner != demo || hasPrefix != demo {
			t.Fatalf("demo=%v: banner=%v title prefix=%v", demo, hasBanner, hasPrefix)
		}
	}
}
