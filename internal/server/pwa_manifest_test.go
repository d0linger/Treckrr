package server

import (
	"encoding/json"
	"io/fs"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/d0linger/treckrr/internal/web"
)

// TestManifestShortcuts keeps the installed app's long-press shortcuts valid:
// same-origin targets and icons that are actually embedded.
func TestManifestShortcuts(t *testing.T) {
	t.Parallel()
	w := httptest.NewRecorder()
	(&Server{}).handleManifest(w, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	if ct := w.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("content type %q", ct)
	}
	var manifest struct {
		ShareTarget struct {
			Action, Method, Enctype string
			Params                  struct {
				Files []struct{ Name string } `json:"files"`
			} `json:"params"`
		} `json:"share_target"`
		Shortcuts []struct {
			Name  string `json:"name"`
			URL   string `json:"url"`
			Icons []struct {
				Src string `json:"src"`
			} `json:"icons"`
		} `json:"shortcuts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &manifest); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	st := manifest.ShareTarget
	if st.Action != shareTargetPath || st.Method != "POST" || st.Enctype != "multipart/form-data" ||
		len(st.Params.Files) != 1 || st.Params.Files[0].Name != "photos" {
		t.Fatalf("share target %+v must post photos to %s", st, shareTargetPath)
	}
	if len(manifest.Shortcuts) < 3 {
		t.Fatalf("shortcuts=%d, want at least 3", len(manifest.Shortcuts))
	}
	for _, sc := range manifest.Shortcuts {
		if sc.Name == "" || !strings.HasPrefix(sc.URL, "/") || strings.HasPrefix(sc.URL, "//") {
			t.Errorf("shortcut %+v must be a named same-origin path", sc)
		}
		for _, icon := range sc.Icons {
			if _, err := fs.Stat(web.StaticFS(), strings.TrimPrefix(icon.Src, "/static/")); err != nil {
				t.Errorf("shortcut %q icon %q is not embedded", sc.Name, icon.Src)
			}
		}
	}
}
