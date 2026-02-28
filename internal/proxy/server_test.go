package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

	if len(s.ListBackends()) != 1 {
		t.Error("ListBackends should return a copy, not the original map")
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
	s.RegisterBackend("qwen3-8b", &Backend{ModelName: "qwen3-8b", Ready: true})
	s.RegisterBackend("glm-4.7-flash", &Backend{ModelName: "glm-4.7-flash", Ready: true})

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

	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("CORS Allow-Origin = %q, want '*'", w.Header().Get("Access-Control-Allow-Origin"))
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
