// Package static serves the embedded React build with SPA fallback so that
// client-side routes resolve to index.html on first load.
package static

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded dist/ tree, with
// SPA fallback: any GET request whose target doesn't match a real file is
// served `index.html` instead. Non-GET requests get 405.
//
// If `index.html` doesn't exist (e.g., dist/ contains only .gitkeep before
// the frontend is built), all unmatched paths get a plain 404.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Try the real file first.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if info, err := fs.Stat(sub, path); err == nil && !info.IsDir() {
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for SPA routes. If even index.html doesn't
		// exist (pre-frontend-build), 404.
		//
		// We read index.html ourselves rather than rewriting r.URL.Path to
		// "/index.html" and re-dispatching to fileServer — http.FileServer
		// "helpfully" 301-redirects any request whose path ends in
		// /index.html to the directory without it, which would bounce the
		// SPA route forever.
		index, err := fs.ReadFile(sub, "index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(index)
	})
}
