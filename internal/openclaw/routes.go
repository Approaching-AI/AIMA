package openclaw

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

// RegisterRoutes returns a function that registers OpenClaw-specific proxy routes.
// Pattern follows internal/fleet/handler.go.
func RegisterRoutes(deps *Deps) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/audio/speech", deps.handleTTS)
		mux.HandleFunc("/v1/audio/transcriptions", deps.handleASR)
		mux.HandleFunc("/v1/images/generations", deps.handleImageGen)
	}
}

// handleTTS proxies TTS requests to the backend serving the requested model.
// Expects JSON body: {"model":"<model-name>", "input":"...", "voice":"..."}
func (d *Deps) handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Read body to extract model name, then reset for proxying
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	r.Body.Close()
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		http.Error(w, `{"error":"missing or invalid model field"}`, http.StatusBadRequest)
		return
	}

	backend := d.findBackend(req.Model)
	if backend == nil {
		http.Error(w, fmt.Sprintf(`{"error":"model %q not found"}`, req.Model), http.StatusNotFound)
		return
	}

	d.reverseProxy(w, r, backend.Address, body)
}

// handleASR proxies ASR (transcription) requests to the backend.
// Expects multipart/form-data with a "model" field.
func (d *Deps) handleASR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// For multipart, we need to parse just the model field.
	// Read the full body so we can forward it as-is.
	body, err := io.ReadAll(io.LimitReader(r.Body, 100<<20)) // 100 MB limit for audio
	r.Body.Close()
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	model := extractModelFromMultipart(r, body)
	if model == "" {
		// Try JSON body as fallback
		var req struct {
			Model string `json:"model"`
		}
		json.Unmarshal(body, &req)
		model = req.Model
	}

	if model == "" {
		http.Error(w, `{"error":"missing model field"}`, http.StatusBadRequest)
		return
	}

	backend := d.findBackend(model)
	if backend == nil {
		http.Error(w, fmt.Sprintf(`{"error":"model %q not found"}`, model), http.StatusNotFound)
		return
	}

	d.reverseProxy(w, r, backend.Address, body)
}

// handleImageGen proxies image generation requests to the backend serving the requested model.
// Expects JSON body: {"model":"<model-name>", "prompt":"...", ...}
func (d *Deps) handleImageGen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	r.Body.Close()
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		http.Error(w, `{"error":"missing or invalid model field"}`, http.StatusBadRequest)
		return
	}

	backend := d.findBackend(req.Model)
	if backend == nil {
		http.Error(w, fmt.Sprintf(`{"error":"model %q not found"}`, req.Model), http.StatusNotFound)
		return
	}

	d.reverseProxy(w, r, backend.Address, body)
}

// findBackend looks up a ready, local backend by model name.
func (d *Deps) findBackend(model string) *Backend {
	backends := d.Backends.ListBackends()
	for _, b := range backends {
		if b.ModelName == model && b.Ready && !b.Remote {
			return b
		}
	}
	return nil
}

// reverseProxy sends the request to the target backend.
func (d *Deps) reverseProxy(w http.ResponseWriter, r *http.Request, targetAddr string, body []byte) {
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("openclaw proxy: invalid backend address", "addr", targetAddr, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = r.URL.Path
			req.Host = target.Host
			req.ContentLength = int64(len(body))
			req.Body = io.NopCloser(strings.NewReader(string(body)))
		},
	}
	proxy.ServeHTTP(w, r)
}

// extractModelFromMultipart parses the "model" field from a multipart form body.
func extractModelFromMultipart(r *http.Request, body []byte) string {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		return ""
	}

	// Find boundary from content-type
	for _, param := range strings.Split(ct, ";") {
		param = strings.TrimSpace(param)
		if strings.HasPrefix(param, "boundary=") {
			boundary := strings.TrimPrefix(param, "boundary=")
			boundary = strings.Trim(boundary, `"`)
			return parseModelFromMultipart(string(body), boundary)
		}
	}
	return ""
}

// parseModelFromMultipart extracts the "model" field value from raw multipart data.
func parseModelFromMultipart(body, boundary string) string {
	parts := strings.Split(body, "--"+boundary)
	for _, part := range parts {
		if strings.Contains(part, `name="model"`) {
			// Value is after the double CRLF
			idx := strings.Index(part, "\r\n\r\n")
			if idx < 0 {
				idx = strings.Index(part, "\n\n")
				if idx < 0 {
					continue
				}
				return strings.TrimSpace(part[idx+2:])
			}
			return strings.TrimSpace(part[idx+4:])
		}
	}
	return ""
}
