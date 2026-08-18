package inferencehttp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
)

const maxTTSRequestBody = 16 << 20

const (
	adapterLiteTTSHTTP = "litetts_http"
	adapterMooERGRPC   = "mooer_grpc"
	adapterVoxCPMClone = "voxcpm_clone"
)

const (
	apiErrorInvalidRequest = "invalid_request_error"
	apiErrorServer         = "server_error"
	apiErrorBackend        = "backend_error"
)

type apiErrorResponse struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    *string `json:"code"`
}

func writeAPIError(w http.ResponseWriter, status int, errorType, message string) {
	if strings.TrimSpace(errorType) == "" {
		errorType = apiErrorInvalidRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiErrorResponse{
		Error: apiError{
			Message: message,
			Type:    errorType,
		},
	})
}

// RegisterRoutes returns a function that registers AIMA inference proxy routes.
// Pattern follows internal/fleet/handler.go.
func RegisterRoutes(deps *Deps) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("/v1/audio/speech", deps.handleTTS)
		mux.HandleFunc("/v1/tts", deps.handleTTS)
		mux.HandleFunc("/v1/audio/transcriptions", deps.handleASR)
		mux.HandleFunc("/v1/audio/quality", deps.handleAudioQuality)
		mux.HandleFunc("/v1/images/generations", deps.handleImageGen)
	}
}

// handleTTS proxies TTS requests to the backend serving the requested model.
// Expects JSON body including "model" and one of "input" or "text".
//
// The request body is forwarded with light normalization:
//   - /v1/audio/speech prefers "input"
//   - /v1/tts prefers "text"
//
// Additional fields such as response_format, speed, reference_audio, and
// reference_text are preserved for backends that support them.
func (d *Deps) handleTTS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, apiErrorInvalidRequest, "method not allowed")
		return
	}

	// Read body to extract model name, then reset for proxying
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTTSRequestBody)) // Allows base64 reference audio clips.
	r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "failed to read request body")
		return
	}

	body, raw, err := normalizeTTSRequestBody(r.URL.Path, body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "invalid JSON body")
		return
	}

	model, _ := raw["model"].(string)
	if model == "" {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "missing or invalid model field")
		return
	}

	backend := d.findBackend(model)
	if backend == nil {
		writeAPIError(w, http.StatusNotFound, apiErrorInvalidRequest, fmt.Sprintf("model %q not found", model))
		return
	}

	if !backend.Remote {
		adapter := d.adapterFor(model, r.URL.Path)
		if adapter == adapterLiteTTSHTTP {
			d.handleLiteTTS(w, r, backend, raw)
			return
		}
		if adapter == adapterVoxCPMClone && hasTTSReferenceAudio(raw) {
			d.handleVoxCPMClone(w, r, backend, raw, body)
			return
		}
	}

	switch r.URL.Path {
	case "/v1/tts":
		d.forwardTTSJSON(w, r, backend, body)
	default:
		d.forwardTTSAudio(w, r, backend, body)
	}
}

func normalizeTTSRequestBody(path string, body []byte) ([]byte, map[string]any, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, err
	}

	switch path {
	case "/v1/audio/speech":
		if input, _ := raw["input"].(string); strings.TrimSpace(input) == "" {
			if text, _ := raw["text"].(string); strings.TrimSpace(text) != "" {
				raw["input"] = text
			}
		}
	case "/v1/tts":
		if text, _ := raw["text"].(string); strings.TrimSpace(text) == "" {
			if input, _ := raw["input"].(string); strings.TrimSpace(input) != "" {
				raw["text"] = input
			}
		}
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, nil, err
	}
	return out, raw, nil
}

// handleASR proxies ASR (transcription) requests to the backend.
// Expects multipart/form-data with a "model" field.
func (d *Deps) handleASR(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, apiErrorInvalidRequest, "method not allowed")
		return
	}

	// For multipart, we need to parse just the model field.
	// Read the full body so we can forward it as-is.
	body, err := io.ReadAll(io.LimitReader(r.Body, 100<<20)) // 100 MB limit for audio
	r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "failed to read request body")
		return
	}

	upload, err := parseASRUpload(r, body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, err.Error())
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
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "missing model field")
		return
	}

	backend := d.findBackend(model)
	if backend == nil {
		writeAPIError(w, http.StatusNotFound, apiErrorInvalidRequest, fmt.Sprintf("model %q not found", model))
		return
	}

	if !backend.Remote && d.adapterFor(model, r.URL.Path) == adapterMooERGRPC {
		d.handleMooERASR(w, r, backend, upload)
		return
	}

	d.forwardASR(w, r, backend, body)
}

// handleAudioQuality routes audio quality scoring to the requested quality backend.
// Remote AIMA peers receive the same public route; local backend target paths
// come from the model's catalog adapter.
func (d *Deps) handleAudioQuality(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, apiErrorInvalidRequest, "method not allowed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 100<<20))
	r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "failed to read request body")
		return
	}

	qualityReq, err := parseQualityRequest(r, body)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, err.Error())
		return
	}
	if qualityReq.Model == "" {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "missing model field")
		return
	}

	backend := d.findBackend(qualityReq.Model)
	if backend == nil {
		writeAPIError(w, http.StatusNotFound, apiErrorInvalidRequest, fmt.Sprintf("model %q not found", qualityReq.Model))
		return
	}

	targetPath := r.URL.Path
	if !backend.Remote {
		var ok bool
		targetPath, ok = d.qualityTargetPath(qualityReq.Model, qualityReq.Upload)
		if !ok {
			writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, fmt.Sprintf("model %q does not define an HTTP adapter for %s", qualityReq.Model, r.URL.Path))
			return
		}
	}

	d.forwardRequest(w, r, backend, targetPath, r.Header.Get("Content-Type"), body)
}

type qualityRequest struct {
	Model  string
	Upload bool
}

func parseQualityRequest(r *http.Request, body []byte) (qualityRequest, error) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil && strings.TrimSpace(r.Header.Get("Content-Type")) != "" {
		return qualityRequest{}, fmt.Errorf("invalid content type")
	}

	switch mediaType {
	case "", "application/json":
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return qualityRequest{}, fmt.Errorf("invalid JSON body")
		}
		return qualityRequest{Model: strings.TrimSpace(req.Model)}, nil
	case "multipart/form-data":
		upload, err := parseASRUpload(r, body)
		if err != nil {
			return qualityRequest{}, err
		}
		if upload == nil {
			return qualityRequest{}, fmt.Errorf("invalid multipart form body")
		}
		return qualityRequest{Model: strings.TrimSpace(upload.Model), Upload: true}, nil
	default:
		return qualityRequest{}, fmt.Errorf("unsupported content type %q", mediaType)
	}
}

// forwardASR forwards the ASR request and cleans the response text.
// vLLM Qwen3-ASR returns text like "language Chinese<asr_text>你好" —
// we strip the metadata prefix to return clean transcription text.
func (d *Deps) forwardASR(w http.ResponseWriter, r *http.Request, backend *Backend, body []byte) {
	targetAddr := backend.Address
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("aima proxy: invalid ASR backend address", "addr", targetAddr, "err", err)
		writeAPIError(w, http.StatusInternalServerError, apiErrorServer, "internal server error")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(target.String(), "/")+r.URL.Path, bytes.NewReader(body))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiErrorServer, "internal server error")
		return
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	if backend.Remote {
		copyAuthHeader(req.Header, r.Header)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("aima proxy: ASR backend request failed", "backend", targetAddr, "err", err)
		writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "failed to read backend response")
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
		return applyStaticRequestRewrites(cat, path, model, engineType, body)
	}
}

func (d *Deps) adapterFor(model, path string) string {
	adapter, ok := d.adapterForPath(model, path)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(adapter.Kind))
}

func (d *Deps) adapterForPath(model, path string) (Adapter, bool) {
	if d == nil || d.Catalog == nil {
		return Adapter{}, false
	}
	for _, adapter := range d.Catalog.Adapters(model) {
		if strings.TrimSpace(adapter.Path) == path {
			return adapter, true
		}
	}
	return Adapter{}, false
}

func (d *Deps) qualityTargetPath(model string, upload bool) (string, bool) {
	adapter, ok := d.adapterForPath(model, "/v1/audio/quality")
	if !ok {
		return "", false
	}
	if upload {
		if targetPath := strings.TrimSpace(adapter.UploadTargetPath); targetPath != "" {
			return targetPath, true
		}
	}
	if targetPath := strings.TrimSpace(adapter.TargetPath); targetPath != "" {
		return targetPath, true
	}
	return "", false
}

// stripOrphanedToolChoice removes tool_choice from JSON request bodies when
// tools is empty or absent. Prevents vLLM 400 errors from OpenAI-compatible clients
// that send tool_choice:"auto" without defining any tools.
func stripOrphanedToolChoice(body []byte) []byte {
	// Fast path: skip full JSON parse if no tool_choice present.
	if !bytes.Contains(body, []byte(`"tool_choice"`)) {
		return body
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return body
	}
	if _, has := req["tool_choice"]; !has {
		return body
	}
	tools, _ := req["tools"].([]any)
	if len(tools) > 0 {
		return body
	}
	delete(req, "tool_choice")
	delete(req, "tools")
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
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

func (d *Deps) forwardTTSAudio(w http.ResponseWriter, r *http.Request, backend *Backend, body []byte) {
	resp, respBody, err := d.callBackend(r, backend, "/v1/audio/speech", r.Header.Get("Content-Type"), body)
	if err != nil {
		slog.Warn("aima proxy: TTS backend request failed", "backend", backend.Address, "err", err)
		writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
		return
	}
	if isMissingRoute(resp.StatusCode) {
		fallbackBody, _, normErr := normalizeTTSRequestBody("/v1/tts", body)
		if normErr == nil {
			resp, respBody, err = d.callBackend(r, backend, "/v1/tts", r.Header.Get("Content-Type"), fallbackBody)
			if err != nil {
				slog.Warn("aima proxy: TTS fallback request failed", "backend", backend.Address, "err", err)
				writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
				return
			}
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && writeAudioFromJSON(w, respBody, body, resp.StatusCode) {
		return
	}
	writeBackendResponse(w, resp, respBody)
}

func (d *Deps) forwardTTSJSON(w http.ResponseWriter, r *http.Request, backend *Backend, body []byte) {
	resp, respBody, err := d.callBackend(r, backend, "/v1/tts", r.Header.Get("Content-Type"), body)
	if err != nil {
		slog.Warn("aima proxy: TTS backend request failed", "backend", backend.Address, "err", err)
		writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
		return
	}
	if isMissingRoute(resp.StatusCode) {
		fallbackBody, _, normErr := normalizeTTSRequestBody("/v1/audio/speech", body)
		if normErr == nil {
			resp, respBody, err = d.callBackend(r, backend, "/v1/audio/speech", r.Header.Get("Content-Type"), fallbackBody)
			if err != nil {
				slog.Warn("aima proxy: TTS fallback request failed", "backend", backend.Address, "err", err)
				writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
				return
			}
		}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && isAudioContent(resp.Header.Get("Content-Type")) {
		writeAudioJSON(w, respBody, body, resp.Header.Get("Content-Type"), resp.StatusCode)
		return
	}
	writeBackendResponse(w, resp, respBody)
}

func (d *Deps) handleVoxCPMClone(w http.ResponseWriter, r *http.Request, backend *Backend, raw map[string]any, requestBody []byte) {
	body, contentType, err := buildVoxCPMCloneRequest(raw)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, err.Error())
		return
	}

	resp, respBody, err := d.callBackend(r, backend, "/v1/clone", contentType, body)
	if err != nil {
		slog.Warn("aima proxy: VoxCPM clone backend request failed", "backend", backend.Address, "err", err)
		writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
		return
	}

	if r.URL.Path == "/v1/tts" && resp.StatusCode >= 200 && resp.StatusCode < 300 && isAudioContent(resp.Header.Get("Content-Type")) {
		writeAudioJSON(w, respBody, requestBody, resp.Header.Get("Content-Type"), resp.StatusCode)
		return
	}
	if r.URL.Path == "/v1/audio/speech" && resp.StatusCode >= 200 && resp.StatusCode < 300 && writeAudioFromJSON(w, respBody, requestBody, resp.StatusCode) {
		return
	}
	writeBackendResponse(w, resp, respBody)
}

func buildVoxCPMCloneRequest(raw map[string]any) ([]byte, string, error) {
	text := extractTTSText(raw)
	if text == "" {
		return nil, "", fmt.Errorf("missing or invalid input field")
	}
	refAudio := firstTTSString(raw, "reference_audio", "ref_audio")
	if refAudio == "" {
		return nil, "", fmt.Errorf("missing or invalid reference_audio field")
	}
	audio, filename, err := decodeReferenceAudio(refAudio)
	if err != nil {
		return nil, "", err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", text); err != nil {
		return nil, "", err
	}
	if refText := firstTTSString(raw, "reference_text", "ref_text"); refText != "" {
		if err := writer.WriteField("ref_text", refText); err != nil {
			return nil, "", err
		}
	}
	if err := writeTTSBackendFormFields(writer, raw); err != nil {
		return nil, "", err
	}
	part, err := writer.CreateFormFile("ref_audio", filename)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(audio); err != nil {
		return nil, "", err
	}
	if err := writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func writeTTSBackendFormFields(writer *multipart.Writer, raw map[string]any) error {
	for key, value := range raw {
		key = strings.TrimSpace(key)
		if key == "" || value == nil || isTTSCloneProtocolField(key) {
			continue
		}
		fieldValue, err := formFieldValue(value)
		if err != nil {
			return fmt.Errorf("encode %s field: %w", key, err)
		}
		if err := writer.WriteField(key, fieldValue); err != nil {
			return err
		}
	}
	return nil
}

func isTTSCloneProtocolField(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "model", "input", "text", "reference_audio", "ref_audio", "reference_text", "ref_text",
		"use_default_reference", "usedefaultreference", "allow_default_reference", "allowdefaultreference":
		return true
	default:
		return false
	}
}

func formFieldValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool, float64, float32, int, int64, int32, uint, uint64, uint32:
		return fmt.Sprint(typed), nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

func hasTTSReferenceAudio(raw map[string]any) bool {
	return firstTTSString(raw, "reference_audio", "ref_audio") != ""
}

func firstTTSString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, _ := raw[key].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func decodeReferenceAudio(value string) ([]byte, string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return decodeReferenceAudioDataURL(value)
	}
	audio, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, "", fmt.Errorf("reference_audio must be a data URL or base64 audio")
	}
	return audio, "reference.wav", nil
}

func decodeReferenceAudioDataURL(value string) ([]byte, string, error) {
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return nil, "", fmt.Errorf("invalid reference_audio data URL")
	}
	meta := value[len("data:"):comma]
	payload := value[comma+1:]
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, "", fmt.Errorf("reference_audio data URL must be base64 encoded")
	}
	audio, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", fmt.Errorf("invalid reference_audio base64 data")
	}

	contentType := strings.TrimSpace(strings.Split(meta, ";")[0])
	format := audioFormatFromContentType(contentType)
	if format == "" {
		format = "wav"
	}
	return audio, "reference." + format, nil
}

func (d *Deps) callBackend(r *http.Request, backend *Backend, targetPath, contentType string, body []byte) (*http.Response, []byte, error) {
	targetAddr := backend.Address
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
	target, err := url.Parse(targetAddr)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(target.String(), "/")+targetPath, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if backend.Remote {
		copyAuthHeader(req.Header, r.Header)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, nil, readErr
	}
	return resp, respBody, nil
}

func copyAuthHeader(dst, src http.Header) {
	for _, value := range src.Values("Authorization") {
		dst.Add("Authorization", value)
	}
}

func isMissingRoute(statusCode int) bool {
	return statusCode == http.StatusNotFound || statusCode == http.StatusMethodNotAllowed
}

func isAudioContent(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(contentType))
	}
	return strings.HasPrefix(strings.ToLower(mediaType), "audio/")
}

func writeBackendResponse(w http.ResponseWriter, resp *http.Response, body []byte) {
	for k, vals := range resp.Header {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func writeAudioJSON(w http.ResponseWriter, audio []byte, requestBody []byte, contentType string, statusCode int) {
	format := audioFormatFromContentType(contentType)
	if format == "" {
		format = ttsResponseFormat(requestBody)
	}
	if format == "" {
		format = "wav"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"audio_base64": base64.StdEncoding.EncodeToString(audio),
		"format":       format,
		"content_type": contentTypeForAudioFormat(format),
	})
}

func writeAudioFromJSON(w http.ResponseWriter, body []byte, requestBody []byte, statusCode int) bool {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return false
	}
	rawAudio, _ := resp["audio_base64"].(string)
	if strings.TrimSpace(rawAudio) == "" {
		return false
	}
	audio, err := base64.StdEncoding.DecodeString(rawAudio)
	if err != nil {
		return false
	}
	format, _ := resp["format"].(string)
	if strings.TrimSpace(format) == "" {
		format = ttsResponseFormat(requestBody)
	}
	w.Header().Set("Content-Type", contentTypeForAudioFormat(format))
	w.WriteHeader(statusCode)
	_, _ = w.Write(audio)
	return true
}

func ttsResponseFormat(body []byte) string {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	for _, key := range []string{"response_format", "format"} {
		if value, _ := req[key].(string); strings.TrimSpace(value) != "" {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	return ""
}

func audioFormatFromContentType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(contentType))
	}
	switch strings.ToLower(mediaType) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/mpeg":
		return "mp3"
	case "audio/ogg":
		return "ogg"
	case "audio/opus":
		return "opus"
	case "audio/flac":
		return "flac"
	case "audio/aac":
		return "aac"
	}
	return ""
}

func contentTypeForAudioFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "mp3":
		return "audio/mpeg"
	case "ogg":
		return "audio/ogg"
	case "opus":
		return "audio/opus"
	case "flac":
		return "audio/flac"
	case "aac":
		return "audio/aac"
	default:
		return "audio/wav"
	}
}

// handleImageGen proxies image generation requests to the backend serving the requested model.
// Expects JSON body: {"model":"<model-name>", "prompt":"...", ...}
func (d *Deps) handleImageGen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, apiErrorInvalidRequest, "method not allowed")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	r.Body.Close()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "failed to read request body")
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Model == "" {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "missing or invalid model field")
		return
	}

	backend := d.findBackend(req.Model)
	if backend == nil {
		writeAPIError(w, http.StatusNotFound, apiErrorInvalidRequest, fmt.Sprintf("model %q not found", req.Model))
		return
	}

	d.reverseProxy(w, r, backend.Address, body)
}

// findBackend looks up a ready backend by model name, preferring local backends
// and falling back to remote AIMA peers for federation.
func (d *Deps) findBackend(model string) *Backend {
	if d == nil || d.Backends == nil {
		return nil
	}
	backends := d.Backends.ListBackends()
	var remote *Backend
	for _, b := range backends {
		if b == nil || !strings.EqualFold(b.ModelName, model) || !b.Ready || strings.TrimSpace(b.Address) == "" {
			continue
		}
		if !b.Remote {
			return b
		}
		if remote == nil {
			remote = b
		}
	}
	return remote
}

// reverseProxy sends the request to the target backend.
func (d *Deps) reverseProxy(w http.ResponseWriter, r *http.Request, targetAddr string, body []byte) {
	// Backend addresses may be stored as "host:port" without scheme
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("aima proxy: invalid backend address", "addr", targetAddr, "err", err)
		writeAPIError(w, http.StatusInternalServerError, apiErrorServer, "internal server error")
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
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			slog.Warn("aima proxy: backend request failed", "backend", targetAddr, "path", req.URL.Path, "err", err)
			writeAPIError(rw, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
		},
	}
	proxy.ServeHTTP(w, r)
}

func (d *Deps) handleLiteTTS(w http.ResponseWriter, r *http.Request, backend *Backend, raw map[string]any) {
	text := extractTTSText(raw)
	if text == "" {
		writeAPIError(w, http.StatusBadRequest, apiErrorInvalidRequest, "missing or invalid input field")
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
		writeAPIError(w, http.StatusInternalServerError, apiErrorServer, "failed to encode LiteTTS request")
		return
	}

	d.forwardRequest(w, r, backend, "/tts/api/v1/generate", "application/json", body)
}

func extractTTSText(raw map[string]any) string {
	if text, _ := raw["text"].(string); strings.TrimSpace(text) != "" {
		return text
	}
	if text, _ := raw["input"].(string); strings.TrimSpace(text) != "" {
		return text
	}
	return ""
}

func (d *Deps) forwardRequest(w http.ResponseWriter, r *http.Request, backend *Backend, targetPath, contentType string, body []byte) {
	targetAddr := backend.Address
	if !strings.HasPrefix(targetAddr, "http://") && !strings.HasPrefix(targetAddr, "https://") {
		targetAddr = "http://" + targetAddr
	}
	target, err := url.Parse(targetAddr)
	if err != nil {
		slog.Error("aima proxy: invalid backend address", "addr", targetAddr, "err", err)
		writeAPIError(w, http.StatusInternalServerError, apiErrorServer, "internal server error")
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, strings.TrimRight(target.String(), "/")+targetPath, bytes.NewReader(body))
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, apiErrorServer, "internal server error")
		return
	}
	req.Header.Set("Content-Type", contentType)
	if backend.Remote {
		copyAuthHeader(req.Header, r.Header)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("aima proxy: backend request failed", "backend", targetAddr, "path", targetPath, "err", err)
		writeAPIError(w, http.StatusBadGateway, apiErrorBackend, "backend unreachable")
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
