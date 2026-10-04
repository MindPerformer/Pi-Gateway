// Package webui serves the embedded admin single-page application.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the built Vue application.
//
// The directory is committed with a placeholder so `go build` works before the
// frontend has been compiled; run `npm run build` in web/ to populate it.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the SPA, falling back to index.html for client-side routes.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "web UI assets are unavailable", http.StatusInternalServerError)
		})
	}

	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.NotFound(w, r)
			return
		}

		requested := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if requested == "" || requested == "." {
			serveIndex(w, r, sub)
			return
		}

		// Serve real assets directly; anything else is a client-side route.
		if f, err := sub.Open(requested); err == nil {
			_ = f.Close()
			if !strings.HasPrefix(requested, "assets/") {
				// index.html must never be cached or the SPA will pin to an old build.
				w.Header().Set("Cache-Control", "no-cache")
			} else {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r, sub)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "web UI is not built yet. Run `npm install && npm run build` in the web/ directory.", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(raw)
}
