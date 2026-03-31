package ui

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
)

// Deps holds optional UI route dependencies.
type Deps struct {
	SupportManifest    func(context.Context) (json.RawMessage, error)
	OnboardingManifest func(context.Context) (json.RawMessage, error)
}

// RegisterRoutes returns a function that registers UI routes on a mux.
func RegisterRoutes(deps *Deps) func(*http.ServeMux) {
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
		redirectStatic := func(path string) {
			mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/ui"+path, http.StatusFound)
			})
		}
		if deps != nil && deps.SupportManifest != nil {
			mux.HandleFunc("GET /ui/api/support-manifest", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-cache, must-revalidate")
				w.Header().Set("Content-Type", "application/json")
				data, err := deps.SupportManifest(r.Context())
				if err != nil {
					w.WriteHeader(http.StatusBadGateway)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				_, _ = w.Write(data)
			})
		}
		if deps != nil && deps.OnboardingManifest != nil {
			mux.HandleFunc("GET /ui/api/onboarding-manifest", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-cache, must-revalidate")
				w.Header().Set("Content-Type", "application/json")
				data, err := deps.OnboardingManifest(r.Context())
				if err != nil {
					w.WriteHeader(http.StatusBadGateway)
					_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
					return
				}
				_, _ = w.Write(data)
			})
		}
		redirectStatic("/favicon.svg")
		redirectStatic("/favicon.ico")
		redirectStatic("/apple-touch-icon.png")
		mux.Handle("GET /ui/", http.StripPrefix("/ui/", noCacheFS))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
}
