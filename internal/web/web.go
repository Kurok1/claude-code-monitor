// Package web embeds the compiled SPA produced by the Vite build in
// frontend/ and exposes it as an http.Handler so the server binary ships
// a single artifact.
//
// Build flow:
//
//	cd frontend && npm run build   # writes here under dist/
//	go build ./cmd/server          # embeds dist/* into the binary
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// ErrNotBuilt is returned when the dist directory has no index.html — the
// frontend has not been built. Callers can decide whether to fail fast or
// fall back to a placeholder response.
var ErrNotBuilt = errors.New("web: frontend dist not built (run `npm run build` in frontend/)")

// AssetFS returns a filesystem rooted at the dist directory.
func AssetFS() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}

// Handler returns an http.Handler that serves the embedded SPA. Requests for
// paths that don't map to a real asset fall through to index.html so React
// Router can handle client-side routes.
func Handler() (http.Handler, error) {
	sub, err := AssetFS()
	if err != nil {
		return nil, err
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, ErrNotBuilt
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			fileServer.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(sub, clean); err != nil {
			serveIndex(w, r, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	data, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
