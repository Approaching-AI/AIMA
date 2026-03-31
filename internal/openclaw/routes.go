package openclaw

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
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

	d.forwardASR(w, r, backend.Address, body)
}

// forwardASR forwards the ASR request and cleans the response text.
// vLLM Qwen3-ASR returns text like "language Chinese<asr_text>你好" —
// we strip the metadata prefix to return clean transcription text.
func (d *Deps) forwardASR(w http.ResponseWriter, r *http.Request, targetAddr string, body []byte) {
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("openclaw proxy: invalid ASR backend address", "addr", targetAddr, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		target.String()+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("openclaw proxy: ASR backend request failed", "backend", targetAddr, "err", err)
		http.Error(w, "backend unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, "failed to read backend response", http.StatusBadGateway)
		return
	}

	// Clean ASR metadata prefix from the text field.
	if resp.StatusCode == http.StatusOK {
		respBody = cleanASRResponse(respBody)
	}

	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue // recalculated below
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(respBody)))
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

// cleanASRResponse strips vLLM Qwen-ASR metadata prefixes from the text field.
// Input:  {"text":"language Chinese<asr_text>你好世界。",...}
// Output: {"text":"你好世界。",...}
func cleanASRResponse(body []byte) []byte {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}
	text, ok := resp["text"].(string)
	if !ok {
		return body
	}
	cleaned := stripASRPrefix(text)
	if cleaned == text {
		return body
	}
	resp["text"] = cleaned
	out, err := json.Marshal(resp)
	if err != nil {
		return body
	}
	return out
}

// stripASRPrefix removes "language <lang><asr_text>" prefix from ASR output.
func stripASRPrefix(text string) string {
	const marker = "<asr_text>"
	if idx := strings.Index(text, marker); idx >= 0 {
		return strings.TrimSpace(text[idx+len(marker):])
	}
	return text
}

func RequestBodyRewriter(cat CatalogReader) func(path, contentType, model, engineType string, body []byte) []byte {
	if cat == nil {
		return nil
	}
	return func(path, contentType, model, engineType string, body []byte) []byte {
		if !isJSONContentType(contentType) {
			return body
		}
		for _, patch := range cat.OpenClawRequestPatches(model) {
			if !matchesRequestPatch(patch, path, engineType) {
				continue
			}
			body = mergeRequestPatchBody(body, patch.Body)
		}
		return body
	}
}

func matchesRequestPatch(patch RequestPatch, path, engineType string) bool {
	if patch.Path != "" && patch.Path != path {
		return false
	}
	if len(patch.EnginePrefixes) == 0 {
		return true
	}
	engineType = strings.ToLower(strings.TrimSpace(engineType))
	for _, prefix := range patch.EnginePrefixes {
		if strings.HasPrefix(engineType, strings.ToLower(strings.TrimSpace(prefix))) {
			return true
		}
	}
	return false
}

func isJSONContentType(contentType string) bool {
	if strings.TrimSpace(contentType) == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	return mediaType == "application/json"
}

func mergeRequestPatchBody(body []byte, patch map[string]any) []byte {
	if len(patch) == 0 {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	mergeJSONDefaults(req, patch)
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

func mergeJSONDefaults(dst, defaults map[string]any) {
	for key, value := range defaults {
		defMap, defIsMap := value.(map[string]any)
		if existing, ok := dst[key]; ok {
			existingMap, existingIsMap := existing.(map[string]any)
			if defIsMap && existingIsMap {
				mergeJSONDefaults(existingMap, defMap)
			}
			continue
		}
		dst[key] = cloneJSONValue(value)
	}
}

func cloneJSONValue(value any) any {
	switch raw := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(raw))
		for key, item := range raw {
			out[key] = cloneJSONValue(item)
		}
		return out
	case []any:
		out := make([]any, len(raw))
		for i, item := range raw {
			out[i] = cloneJSONValue(item)
		}
		return out
	default:
		return raw
	}
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
