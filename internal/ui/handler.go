package ui

import (
	"io/fs"
	"net/http"
)

// RegisterRoutes returns a function that registers UI static file routes on a mux.
func RegisterRoutes() func(*http.ServeMux) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// go:embed guarantees "static" exists at compile time; this cannot fail.
		panic("ui: embed sub fs: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	// Wrap file server to prevent caching of embedded files (no content hash).
	noCacheFS := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		fileServer.ServeHTTP(w, r)
	})
	return func(mux *http.ServeMux) {
		mux.Handle("GET /ui/", http.StripPrefix("/ui/", noCacheFS))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
}
