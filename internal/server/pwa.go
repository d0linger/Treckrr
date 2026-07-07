package server

import (
	"io/fs"
	"net/http"
	"strings"

	"treckrr/internal/web"
)

// handleManifest serves the PWA manifest from the embedded static assets.
func (s *Server) handleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	serveStaticFile(w, r, "manifest.webmanifest")
}

// handleServiceWorker serves the service worker at the site root so its scope
// covers the whole application. The cache-version placeholder is replaced with
// the current asset hash so a new build invalidates the old caches.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(web.StaticFS(), "sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	body := strings.ReplaceAll(string(data), "__CACHE_VERSION__", web.AssetVersion())
	w.Header().Set("Content-Type", "text/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(body))
}

// handleOffline is the fallback page shown by the service worker when offline.
func (s *Server) handleOffline(w http.ResponseWriter, r *http.Request) {
	data := pageData{"Title": "Offline", "Theme": themeFromCookie(r), "CSRF": s.csrfToken(r)}
	s.render(w, r, "offline", data)
}

func serveStaticFile(w http.ResponseWriter, r *http.Request, name string) {
	data, err := fs.ReadFile(web.StaticFS(), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}
