package ui

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Deps holds optional UI route dependencies.
type Deps struct {
	SupportManifest    func(context.Context) (json.RawMessage, error)
	OnboardingManifest func(context.Context) (json.RawMessage, error)
	APIKey             func(context.Context) string
}

// RegisterRoutes returns a function that registers UI routes on a mux.
func RegisterRoutes(deps *Deps) func(*http.ServeMux) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// go:embed guarantees "static" exists at compile time; this cannot fail.
		panic("ui: embed sub fs: " + err.Error())
	}
	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		panic("ui: embed index.html: " + err.Error())
	}
	fileServer := http.FileServer(http.FS(sub))
	// Wrap file server to prevent caching of embedded files (no content hash).
	noCacheFS := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		fileServer.ServeHTTP(w, r)
	})
	staticUI := http.StripPrefix("/ui/", noCacheFS)
	serveIndex := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		body := string(indexHTML)
		if deps != nil && deps.APIKey != nil && isLoopbackUIRequest(r) {
			if key := strings.TrimSpace(deps.APIKey(r.Context())); key != "" {
				if encoded, err := json.Marshal(key); err == nil {
					body = strings.Replace(body,
						`window.__AIMA_BOOTSTRAP_API_KEY__ = "";`,
						`window.__AIMA_BOOTSTRAP_API_KEY__ = `+string(encoded)+`;`,
						1,
					)
				}
			}
		}
		_, _ = w.Write([]byte(body))
	}
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
		mux.HandleFunc("GET /ui/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ui/" || r.URL.Path == "/ui/index.html" {
				serveIndex(w, r)
				return
			}
			staticUI.ServeHTTP(w, r)
		})
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/ui/", http.StatusFound)
		})
	}
}

func isLoopbackUIRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	return isLoopbackHost(hostOnly(r.Host)) && isLoopbackHost(hostOnly(r.RemoteAddr))
}

func hostOnly(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return strings.Trim(host, "[]")
	}
	return strings.Trim(addr, "[]")
}

func isLoopbackHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
