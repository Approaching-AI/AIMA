package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestBackend(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func TestNewServer_Defaults(t *testing.T) {
	s := NewServer()
	want := fmt.Sprintf(":%d", DefaultPort)
	if s.addr != want {
		t.Errorf("default addr = %q, want %q", s.addr, want)
	}
	if s.routes == nil {
		t.Error("routes map should be initialized")
	}
}

func TestNewServer_WithAddr(t *testing.T) {
	s := NewServer(WithAddr(":9090"))
	if s.addr != ":9090" {
		t.Errorf("addr = %q, want ':9090'", s.addr)
	}
}

func TestRegisterAndRemoveBackend(t *testing.T) {
	s := NewServer()
	b := &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    "10.42.0.5:8000",
		BasePath:   "/v1",
		Ready:      true,
	}

	s.RegisterBackend("qwen3-8b", b)
	backends := s.ListBackends()
	if len(backends) != 1 {
		t.Fatalf("expected 1 backend, got %d", len(backends))
	}
	if backends["qwen3-8b"].Address != "10.42.0.5:8000" {
		t.Errorf("backend address = %q, want '10.42.0.5:8000'", backends["qwen3-8b"].Address)
	}

	s.RemoveBackend("qwen3-8b")
	backends = s.ListBackends()
	if len(backends) != 0 {
		t.Errorf("expected 0 backends after removal, got %d", len(backends))
	}
}

func TestListBackends_ReturnsCopy(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("model-a", &Backend{ModelName: "model-a", Address: "1.2.3.4:8000"})

	backends := s.ListBackends()
	backends["model-b"] = &Backend{ModelName: "model-b"} // modify the returned map
	backends["model-a"].Address = "9.9.9.9:9000"         // modify returned backend object

	current := s.ListBackends()
	if len(current) != 1 {
		t.Error("ListBackends should return a copy, not the original map")
	}
	if current["model-a"].Address != "1.2.3.4:8000" {
		t.Error("ListBackends should return copied backend values, not shared pointers")
	}
}

func TestRegisterBackend_ClonesInput(t *testing.T) {
	s := NewServer()
	original := &Backend{
		ModelName:  "model-a",
		EngineType: "vllm",
		Address:    "1.2.3.4:8000",
		Ready:      true,
	}
	s.RegisterBackend("model-a", original)

	// Mutate caller-owned object after registration.
	original.Address = "9.9.9.9:9000"
	original.Ready = false

	got := s.ListBackends()["model-a"]
	if got.Address != "1.2.3.4:8000" || !got.Ready {
		t.Fatalf("RegisterBackend should copy input; got %+v", got)
	}
}

func TestHealthEndpoint(t *testing.T) {
	s := NewServer()
	handler := s.handler()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /health status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode /health response: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("health status = %q, want 'ok'", resp["status"])
	}
}

func TestStatusEndpoint(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    "10.42.0.5:8000",
		Ready:      true,
	})

	handler := s.handler()
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /status status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode /status response: %v", err)
	}
	models, ok := resp["models"].([]interface{})
	if !ok || len(models) != 1 {
		t.Errorf("expected 1 model in status, got %v", resp["models"])
	}
}

func TestModelsEndpoint(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("qwen3-8b", &Backend{ModelName: "qwen3-8b", Address: "10.0.0.1:8000", Ready: true})
	s.RegisterBackend("glm-4.7-flash", &Backend{ModelName: "glm-4.7-flash", Address: "10.0.0.2:8000", Ready: true})

	handler := s.handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("GET /v1/models status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode /v1/models: %v", err)
	}
	if resp.Object != "list" {
		t.Errorf("object = %q, want 'list'", resp.Object)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 models, got %d", len(resp.Data))
	}
	for _, m := range resp.Data {
		if m.Object != "model" {
			t.Errorf("model object = %q, want 'model'", m.Object)
		}
		if m.OwnedBy != "aima" {
			t.Errorf("owned_by = %q, want 'aima'", m.OwnedBy)
		}
	}
}

func TestModelsEndpoint_FiltersNotReady(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("ready-model", &Backend{ModelName: "ready-model", Address: "10.0.0.1:8000", Ready: true})
	s.RegisterBackend("not-ready", &Backend{ModelName: "not-ready", Ready: false})
	s.RegisterBackend("no-address", &Backend{ModelName: "no-address", Ready: true}) // Ready but no Address

	handler := s.handler()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 ready model, got %d", len(resp.Data))
	}
	if resp.Data[0].ID != "ready-model" {
		t.Errorf("model = %q, want ready-model", resp.Data[0].ID)
	}
}

func TestChatCompletions_RoutesToCorrectBackend(t *testing.T) {
	// Create a mock backend that echoes what it received
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1","choices":[{"message":{"content":"hello"}}]}`)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    addr,
		BasePath:   "",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /v1/chat/completions status = %d, want %d", w.Code, http.StatusOK)
	}

	// Verify debug headers
	if w.Header().Get("X-Aima-Model") != "qwen3-8b" {
		t.Errorf("X-Aima-Model = %q, want 'qwen3-8b'", w.Header().Get("X-Aima-Model"))
	}
	if w.Header().Get("X-Aima-Engine") != "vllm" {
		t.Errorf("X-Aima-Engine = %q, want 'vllm'", w.Header().Get("X-Aima-Engine"))
	}
}

func TestChatCompletions_AppliesRequestRewriter(t *testing.T) {
	var receivedBody map[string]any
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if err := json.Unmarshal(data, &receivedBody); err != nil {
			t.Fatalf("Unmarshal backend body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1","choices":[{"message":{"content":"ok"}}]}`)
	})
	defer backend.Close()

	s := NewServer(WithRequestRewriter(func(path, contentType, model, engineType string, body []byte) []byte {
		if path != "/v1/chat/completions" || model != "qwen3.5-9b" || engineType != "vllm-nightly" {
			return body
		}
		return []byte(`{"model":"qwen3.5-9b","messages":[],"chat_template_kwargs":{"enable_thinking":false}}`)
	}))
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3.5-9b", &Backend{
		ModelName:  "qwen3.5-9b",
		EngineType: "vllm-nightly",
		Address:    addr,
		Ready:      true,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen3.5-9b","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	kwargs, ok := receivedBody["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatal("chat_template_kwargs not injected")
	}
	if kwargs["enable_thinking"] != false {
		t.Fatalf("enable_thinking = %v, want false", kwargs["enable_thinking"])
	}
}

func TestChatCompletions_UnknownModelReturns404(t *testing.T) {
	s := NewServer()
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    "127.0.0.1:9999",
		Ready:      true,
	})

	handler := s.handler()
	// Request with unknown model — should return 404 even if only 1 backend exists
	body := `{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown model, got %d", w.Code)
	}
}

func TestChatCompletions_NotReadyModelReturns503(t *testing.T) {
	backendCalls := 0
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    addr,
		Ready:      false,
	})

	handler := s.handler()
	body := `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for not-ready model, got %d", w.Code)
	}
	if backendCalls != 0 {
		t.Errorf("backend should not be called for not-ready model, got %d calls", backendCalls)
	}
}

func TestChatCompletions_ModelNotFound(t *testing.T) {
	s := NewServer()
	// Register 2 backends so no default fallback
	s.RegisterBackend("model-a", &Backend{ModelName: "model-a", Address: "1.2.3.4:8000"})
	s.RegisterBackend("model-b", &Backend{ModelName: "model-b", Address: "5.6.7.8:8000"})

	handler := s.handler()
	body := `{"model":"nonexistent","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown model, got %d", w.Code)
	}
}

func TestChatCompletions_NoBackends(t *testing.T) {
	s := NewServer()
	handler := s.handler()
	body := `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404 with no backends, got %d", w.Code)
	}
}

func TestChatCompletions_InvalidJSON(t *testing.T) {
	s := NewServer()
	handler := s.handler()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid JSON, got %d", w.Code)
	}
}

func TestCompletions_RoutesToBackend(t *testing.T) {
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify the path is forwarded correctly
		if r.URL.Path != "/v1/completions" {
			t.Errorf("backend received path %q, want '/v1/completions'", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"cmpl-1","choices":[{"text":"hello"}]}`)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    addr,
		BasePath:   "/v1",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"qwen3-8b","prompt":"Hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /v1/completions status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestEmbeddings_RoutesToBackend(t *testing.T) {
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"embedding":[0.1,0.2]}]}`)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("embed-model", &Backend{
		ModelName:  "embed-model",
		EngineType: "vllm",
		Address:    addr,
		BasePath:   "/v1",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"embed-model","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("POST /v1/embeddings status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAudioSpeech_RoutesToBackend(t *testing.T) {
	var (
		receivedPath string
		receivedBody string
	)
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		data, _ := io.ReadAll(r.Body)
		receivedBody = string(data)
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write([]byte("RIFF"))
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("litetts-mnn", &Backend{
		ModelName:  "litetts-mnn",
		EngineType: "litetts",
		Address:    addr,
		BasePath:   "/tts/api/v1",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"litetts-mnn","input":"hello","voice":"demo"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/audio/speech", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /v1/audio/speech status = %d, want %d", w.Code, http.StatusOK)
	}
	if receivedPath != "/tts/api/v1/audio/speech" {
		t.Fatalf("backend received path %q, want %q", receivedPath, "/tts/api/v1/audio/speech")
	}
	if !strings.Contains(receivedBody, `"model":"litetts-mnn"`) {
		t.Fatalf("backend body missing model, got %q", receivedBody)
	}
}

func TestAudioTranscriptions_MultipartRoutesToBackend(t *testing.T) {
	var (
		receivedPath        string
		receivedContentType string
		receivedBody        string
	)
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		receivedContentType = r.Header.Get("Content-Type")
		data, _ := io.ReadAll(r.Body)
		receivedBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"text":"hello"}`)
	})
	defer backend.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "mooer-asr-1.5b"); err != nil {
		t.Fatalf("WriteField(model): %v", err)
	}
	fileWriter, err := writer.CreateFormFile("file", "sample.wav")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := fileWriter.Write([]byte("RIFF....WAVE")); err != nil {
		t.Fatalf("write file body: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("writer.Close(): %v", err)
	}

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("mooer-asr-1.5b", &Backend{
		ModelName:  "mooer-asr-1.5b",
		EngineType: "mooer-asr-musa",
		Address:    addr,
		BasePath:   "",
		Ready:      true,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", bytes.NewReader(body.Bytes()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /v1/audio/transcriptions status = %d, want %d", w.Code, http.StatusOK)
	}
	if receivedPath != "/v1/audio/transcriptions" {
		t.Fatalf("backend received path %q, want %q", receivedPath, "/v1/audio/transcriptions")
	}
	if !strings.HasPrefix(receivedContentType, "multipart/form-data; boundary=") {
		t.Fatalf("backend content type = %q, want multipart boundary", receivedContentType)
	}
	if !strings.Contains(receivedBody, "mooer-asr-1.5b") || !strings.Contains(receivedBody, "sample.wav") {
		t.Fatalf("backend body missing multipart fields, got %q", receivedBody)
	}
}

func TestImagesGenerations_RoutesToBackend(t *testing.T) {
	var receivedPath string
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":[{"b64_json":"abc"}]}`)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("z-image", &Backend{
		ModelName:  "z-image",
		EngineType: "z-image-diffusers",
		Address:    addr,
		BasePath:   "",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"z-image","prompt":"draw a cat"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST /v1/images/generations status = %d, want %d", w.Code, http.StatusOK)
	}
	if receivedPath != "/v1/images/generations" {
		t.Fatalf("backend received path %q, want %q", receivedPath, "/v1/images/generations")
	}
}

func TestSSEStreaming(t *testing.T) {
	// Simulate an SSE streaming backend
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected ResponseWriter to be http.Flusher")
		}

		events := []string{
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":"Hello"}}]}`,
			`data: {"id":"chatcmpl-1","choices":[{"delta":{"content":" world"}}]}`,
			`data: [DONE]`,
		}
		for _, event := range events {
			fmt.Fprintf(w, "%s\n\n", event)
			flusher.Flush()
		}
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    addr,
		BasePath:   "",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("SSE status = %d, want %d", w.Code, http.StatusOK)
	}

	respBody := w.Body.String()
	if !strings.Contains(respBody, "data: [DONE]") {
		t.Errorf("expected SSE response to contain 'data: [DONE]', got: %s", respBody)
	}
	if !strings.Contains(respBody, "Hello") {
		t.Errorf("expected SSE response to contain 'Hello', got: %s", respBody)
	}
}

func TestCORSHeaders(t *testing.T) {
	s := NewServer()
	handler := s.handler()

	req := httptest.NewRequest(http.MethodOptions, "/v1/chat/completions", nil)
	req.Header.Set("Origin", "http://localhost:3000")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3000" {
		t.Errorf("CORS Allow-Origin = %q, want 'http://localhost:3000'", w.Header().Get("Access-Control-Allow-Origin"))
	}
	if w.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Error("expected Access-Control-Allow-Methods header")
	}
}

func TestStartAndShutdown(t *testing.T) {
	s := NewServer(WithAddr("127.0.0.1:0")) // port 0 for random free port

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- s.Start(ctx)
	}()

	// Give server time to start
	time.Sleep(50 * time.Millisecond)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()

	if err := s.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	cancel()

	err := <-errCh
	if err != nil && err != http.ErrServerClosed {
		t.Errorf("Start() returned unexpected error: %v", err)
	}
}

func TestProxyForwardsRequestBody(t *testing.T) {
	var receivedBody string
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		receivedBody = string(data)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1"}`)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    addr,
		BasePath:   "",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"qwen3-8b","messages":[{"role":"user","content":"test body forwarding"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !strings.Contains(receivedBody, "test body forwarding") {
		t.Errorf("backend did not receive request body, got: %s", receivedBody)
	}
}

func TestAPIKeyHotReload(t *testing.T) {
	s := NewServer()
	handler := s.handler() // build handler once, just like Start() does

	doReq := func(token string) int {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}

	// No key configured → all requests pass
	if code := doReq(""); code != http.StatusOK {
		t.Fatalf("no key: expected 200, got %d", code)
	}

	// Set key after handler was built → should take effect immediately
	s.SetAPIKey("secret-1")
	if code := doReq(""); code != http.StatusUnauthorized {
		t.Fatalf("after set key, no auth: expected 401, got %d", code)
	}
	if code := doReq("wrong"); code != http.StatusUnauthorized {
		t.Fatalf("after set key, wrong auth: expected 401, got %d", code)
	}
	if code := doReq("secret-1"); code != http.StatusOK {
		t.Fatalf("after set key, correct auth: expected 200, got %d", code)
	}

	// Rotate key → old key rejected, new key accepted
	s.SetAPIKey("secret-2")
	if code := doReq("secret-1"); code != http.StatusUnauthorized {
		t.Fatalf("after rotate, old key: expected 401, got %d", code)
	}
	if code := doReq("secret-2"); code != http.StatusOK {
		t.Fatalf("after rotate, new key: expected 200, got %d", code)
	}

	// Clear key → all requests pass again
	s.SetAPIKey("")
	if code := doReq(""); code != http.StatusOK {
		t.Fatalf("after clear key: expected 200, got %d", code)
	}

	// /health always exempt regardless of key
	s.SetAPIKey("secret-3")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/health should be exempt from auth, got %d", w.Code)
	}
}

func TestBackendWithBasePath(t *testing.T) {
	var receivedPath string
	backend := newTestBackend(t, func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"chatcmpl-1"}`)
	})
	defer backend.Close()

	s := NewServer()
	addr := strings.TrimPrefix(backend.URL, "http://")
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName:  "qwen3-8b",
		EngineType: "vllm",
		Address:    addr,
		BasePath:   "/v1",
		Ready:      true,
	})

	handler := s.handler()
	body := `{"model":"qwen3-8b","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// The proxy should forward to {basePath}/chat/completions
	if receivedPath != "/v1/chat/completions" {
		t.Errorf("backend received path %q, want '/v1/chat/completions'", receivedPath)
	}
}
