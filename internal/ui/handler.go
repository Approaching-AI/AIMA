package ui

import (
	"io/fs"
	"log"
	"net/http"
)

// RegisterRoutes returns a function that registers UI static file routes on a mux.
func RegisterRoutes() func(*http.ServeMux) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("ui: embed sub fs: %v", err) // compile-time guarantee; should never happen
	}
	fileServer := http.FileServer(http.FS(sub))
	return func(mux *http.ServeMux) {
		mux.Handle("GET /ui/", http.StripPrefix("/ui/", fileServer))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
}
