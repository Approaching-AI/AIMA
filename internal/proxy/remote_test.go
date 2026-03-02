package proxy

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestSyncRemoteBackends_SkipsLocalModels(t *testing.T) {
	s := NewServer()
	// Register a local backend
	s.RegisterBackend("qwen3-8b", &Backend{
		ModelName: "qwen3-8b",
		Address:   "10.42.0.73:8000",
		Ready:     true,
		Remote:    false,
	})

	// Start a mock remote server that also has qwen3-8b
	ts := newModelServer(t, []string{"qwen3-8b", "llama3-70b"})
	defer ts.Close()

	addr, port := splitHostPort(t, ts)
	services := []DiscoveredService{
		{Name: "remote-gpu", AddrV4: addr, Port: port},
	}

	SyncRemoteBackends(context.Background(), s, services, 0)

	backends := s.ListBackends()

	// qwen3-8b should remain local, not overwritten
	b := backends["qwen3-8b"]
	if b == nil {
		t.Fatal("expected qwen3-8b backend")
	}
	if b.Remote {
		t.Error("qwen3-8b should remain local (Remote=false)")
	}
	if b.Address != "10.42.0.73:8000" {
		t.Errorf("qwen3-8b address = %q, want local address", b.Address)
	}

	// llama3-70b should be registered as remote
	b2 := backends["llama3-70b"]
	if b2 == nil {
		t.Fatal("expected llama3-70b backend")
	}
	if !b2.Remote {
		t.Error("llama3-70b should be Remote=true")
	}
}

func TestSyncRemoteBackends_RegistersRemote(t *testing.T) {
	s := NewServer()

	ts := newModelServer(t, []string{"qwen3.5-35b-a3b", "qwen3-8b"})
	defer ts.Close()

	addr, port := splitHostPort(t, ts)
	services := []DiscoveredService{
		{Name: "gpu-server", AddrV4: addr, Port: port},
	}

	SyncRemoteBackends(context.Background(), s, services, 0)

	backends := s.ListBackends()
	if len(backends) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(backends))
	}

	for _, model := range []string{"qwen3.5-35b-a3b", "qwen3-8b"} {
		b, ok := backends[model]
		if !ok {
			t.Errorf("expected backend for %s", model)
			continue
		}
		if !b.Remote {
			t.Errorf("%s should be Remote=true", model)
		}
		if !b.Ready {
			t.Errorf("%s should be Ready=true", model)
		}
	}
}

func TestSyncRemoteBackends_CleansStale(t *testing.T) {
	s := NewServer()
	// Pre-register a remote backend that will disappear
	s.RegisterBackend("old-remote-model", &Backend{
		ModelName: "old-remote-model",
		Address:   "192.168.1.100:8080",
		Ready:     true,
		Remote:    true,
	})
	// Also register a local backend that should survive
	s.RegisterBackend("local-model", &Backend{
		ModelName: "local-model",
		Address:   "10.42.0.50:8000",
		Ready:     true,
		Remote:    false,
	})

	// New discovery: only has new-model, old-remote-model is gone
	ts := newModelServer(t, []string{"new-model"})
	defer ts.Close()

	addr, port := splitHostPort(t, ts)
	SyncRemoteBackends(context.Background(), s, []DiscoveredService{
		{Name: "new-server", AddrV4: addr, Port: port},
	}, 0)

	backends := s.ListBackends()

	// old-remote-model should be cleaned up
	if _, ok := backends["old-remote-model"]; ok {
		t.Error("stale remote backend 'old-remote-model' should have been removed")
	}

	// local-model should survive
	if _, ok := backends["local-model"]; !ok {
		t.Error("local backend 'local-model' should not be removed")
	}

	// new-model should be registered
	if _, ok := backends["new-model"]; !ok {
		t.Error("new-model should be registered")
	}
}

func TestQueryRemoteModels(t *testing.T) {
	ts := newModelServer(t, []string{"model-a", "model-b", "model-c"})
	defer ts.Close()

	addr, port := splitHostPort(t, ts)
	models := queryRemoteModels(context.Background(), addr, port, "")

	if len(models) != 3 {
		t.Fatalf("expected 3 models, got %d: %v", len(models), models)
	}

	want := map[string]bool{"model-a": true, "model-b": true, "model-c": true}
	for _, m := range models {
		if !want[m] {
			t.Errorf("unexpected model %q", m)
		}
	}
}

func TestQueryRemoteModels_Unreachable(t *testing.T) {
	// Query a port that nothing is listening on
	models := queryRemoteModels(context.Background(), "127.0.0.1", 1, "")
	if models != nil {
		t.Errorf("expected nil for unreachable host, got %v", models)
	}
}

func TestSyncRemoteBackends_SkipsSelf(t *testing.T) {
	s := NewServer()

	ts := newModelServer(t, []string{"qwen3-8b"})
	defer ts.Close()

	addr, port := splitHostPort(t, ts)
	services := []DiscoveredService{
		{Name: "self", AddrV4: addr, Port: port},
	}

	// Pass localPort == port so the service is recognized as self
	SyncRemoteBackends(context.Background(), s, services, port)

	backends := s.ListBackends()
	if len(backends) != 0 {
		t.Fatalf("expected 0 backends (self filtered), got %d", len(backends))
	}
}

func TestQueryRemoteModels_WithAPIKey(t *testing.T) {
	// Server that requires Bearer auth
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]string{{"id": "secure-model", "object": "model"}},
		})
	}))
	defer ts.Close()

	addr, port := splitHostPort(t, ts)

	// Without key → no models
	noKey := queryRemoteModels(context.Background(), addr, port, "")
	if len(noKey) != 0 {
		t.Errorf("expected 0 models without key, got %d", len(noKey))
	}

	// With correct key → 1 model
	withKey := queryRemoteModels(context.Background(), addr, port, "test-key")
	if len(withKey) != 1 || withKey[0] != "secure-model" {
		t.Errorf("expected [secure-model] with key, got %v", withKey)
	}
}

// newModelServer creates a test HTTP server that serves /v1/models in OpenAI format.
func newModelServer(t *testing.T, modelNames []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		data := make([]map[string]string, len(modelNames))
		for i, name := range modelNames {
			data[i] = map[string]string{"id": name, "object": "model", "owned_by": "aima"}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
	})
	return httptest.NewServer(mux)
}

// splitHostPort extracts the host and port from a httptest.Server.
func splitHostPort(t *testing.T, ts *httptest.Server) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(ts.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port
}
