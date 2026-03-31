package openclaw

import (
	"bytes"
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
//
// The proxy strips unsupported response_format values before forwarding.
// Some TTS backends (e.g. qwen3-tts FastAPI) only support "wav" output;
// clients like OpenClaw always request "mp3". Removing the field lets the
// backend use its default format, avoiding 422 errors.
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

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		http.Error(w, `{"error":"invalid JSON body"}`, http.StatusBadRequest)
		return
	}

	model, _ := raw["model"].(string)
	if model == "" {
		http.Error(w, `{"error":"missing or invalid model field"}`, http.StatusBadRequest)
		return
	}

	// Strip response_format if it's not "wav" — our TTS backends only produce WAV.
	// Callers get valid audio regardless; most HTTP audio clients handle WAV fine.
	if fmt, ok := raw["response_format"].(string); ok && fmt != "wav" {
		delete(raw, "response_format")
		body, _ = json.Marshal(raw)
	}

	backend := d.findBackend(model)
	if backend == nil {
		http.Error(w, fmt.Sprintf(`{"error":"model %q not found"}`, model), http.StatusNotFound)
		return
	}

	if backend.EngineType == "litetts" {
		d.handleLiteTTS(w, r, backend, raw)
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

	upload, err := parseASRUpload(r, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	model := ""
	if upload != nil {
		model = upload.Model
	}
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

	if isMooERBackend(backend) {
		d.handleMooERASR(w, r, backend, upload)
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
	// Backend addresses may be stored as "host:port" without scheme
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
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

func (d *Deps) handleLiteTTS(w http.ResponseWriter, r *http.Request, backend *Backend, raw map[string]any) {
	text, _ := raw["input"].(string)
	if strings.TrimSpace(text) == "" {
		http.Error(w, `{"error":"missing or invalid input field"}`, http.StatusBadRequest)
		return
	}

	speaker, _ := raw["voice"].(string)
	if speaker == "" || speaker == "default" {
		speaker = "AIBC006_lite"
	}

	payload := map[string]any{
		"text":    text,
		"speaker": speaker,
		"version": "v2.0",
	}
	body, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":"failed to encode LiteTTS request"}`, http.StatusInternalServerError)
		return
	}

	d.forwardRequest(w, r, backend.Address, "/tts/api/v1/generate", "application/json", body)
}

func (d *Deps) forwardRequest(w http.ResponseWriter, r *http.Request, targetAddr, targetPath, contentType string, body []byte) {
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("openclaw proxy: invalid backend address", "addr", targetAddr, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String()+targetPath, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("openclaw proxy: backend request failed", "backend", targetAddr, "path", targetPath, "err", err)
		http.Error(w, "backend unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
