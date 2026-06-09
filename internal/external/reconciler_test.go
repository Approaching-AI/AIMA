package external

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
	"github.com/jguan/aima/internal/proxy"
)

func TestReconcileBackends(t *testing.T) {
	proxyServer := proxy.NewServer()
	service := Overview{
		BaseURL: "http://127.0.0.1:8004",
		Kind:    "openai",
		Models:  []string{"whisper-large-v3-hf"},
	}

	imported, err := ReconcileBackends(proxyServer, service, service.Models)
	if err != nil {
		t.Fatalf("ReconcileBackends: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}

	backends := proxyServer.ListBackends()
	backend := backends["whisper-large-v3-hf"]
	if backend == nil {
		t.Fatal("backend for whisper-large-v3-hf missing")
	}
	if !backend.External {
		t.Fatal("External = false, want true")
	}
	if backend.Address != "127.0.0.1:8004" {
		t.Fatalf("Address = %q, want 127.0.0.1:8004", backend.Address)
	}
	if backend.BasePath != "" {
		t.Fatalf("BasePath = %q, want empty", backend.BasePath)
	}
	if backend.Scheme != "http" {
		t.Fatalf("Scheme = %q, want http", backend.Scheme)
	}
	if backend.UpstreamModel != "whisper-large-v3-hf" {
		t.Fatalf("UpstreamModel = %q, want whisper-large-v3-hf", backend.UpstreamModel)
	}
}

func TestReconcileBackendsPreservesHTTPSScheme(t *testing.T) {
	proxyServer := proxy.NewServer()
	service := Overview{
		BaseURL: "https://example.com/v1",
		Kind:    "openai",
		Models:  []string{"secure-model"},
	}

	imported, err := ReconcileBackends(proxyServer, service, service.Models)
	if err != nil {
		t.Fatalf("ReconcileBackends: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}
	backend := proxyServer.ListBackends()["secure-model"]
	if backend == nil {
		t.Fatal("secure-model backend missing")
	}
	if backend.Scheme != "https" {
		t.Fatalf("Scheme = %q, want https", backend.Scheme)
	}
	if backend.Address != "example.com" {
		t.Fatalf("Address = %q, want example.com", backend.Address)
	}
	if backend.BasePath != "" {
		t.Fatalf("BasePath = %q, want empty for /v1 base URL", backend.BasePath)
	}
}

func TestReconciledNestedV1BasePathForwardsThroughProxy(t *testing.T) {
	type chatRequest struct {
		Path  string
		Model string
	}

	chatRequests := make(chan chatRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data":   []map[string]any{{"id": "nested-model"}},
			})
		case "/api/v1/chat/completions":
			var req struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			chatRequests <- chatRequest{Path: r.URL.Path, Model: req.Model}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "chatcmpl-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	ctx := context.Background()
	svc, err := Probe(ctx, upstream.URL+"/api/v1/models", upstream.Client())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if svc.BaseURL != upstream.URL+"/api/v1" {
		t.Fatalf("BaseURL = %q, want %q", svc.BaseURL, upstream.URL+"/api/v1")
	}

	proxyServer := proxy.NewServer(proxy.WithAddr("127.0.0.1:0"))
	imported, err := ReconcileBackends(proxyServer, OverviewFromScan(svc), svc.Models)
	if err != nil {
		t.Fatalf("ReconcileBackends: %v", err)
	}
	if imported != 1 {
		t.Fatalf("imported = %d, want 1", imported)
	}

	proxyCtx, cancelProxy := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	proxyErr := make(chan error, 1)
	proxyServer.SetOnReady(func(addr string) {
		ready <- addr
	})
	go func() {
		proxyErr <- proxyServer.Start(proxyCtx)
	}()
	defer func() {
		cancelProxy()
		select {
		case err := <-proxyErr:
			if err != nil {
				t.Errorf("proxy Start: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("proxy did not stop after context cancellation")
		}
	}()

	var proxyAddr string
	select {
	case proxyAddr = <-ready:
	case err := <-proxyErr:
		t.Fatalf("proxy stopped before ready: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("proxy did not become ready")
	}

	body := `{"model":"nested-model","messages":[{"role":"user","content":"hi"}]}`
	requestCtx, cancelRequest := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelRequest()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, "http://"+proxyAddr+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	select {
	case got := <-chatRequests:
		if got.Path != "/api/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want /api/v1/chat/completions", got.Path)
		}
		if got.Model != "nested-model" {
			t.Fatalf("upstream model = %q, want nested-model", got.Model)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive chat request")
	}
}

func TestReconcileBackendsRejectsHealthzService(t *testing.T) {
	proxyServer := proxy.NewServer()
	service := Overview{
		BaseURL: "http://127.0.0.1:8009",
		Kind:    "healthz",
		Models:  []string{"SenseVoiceSmall", "pyannote"},
	}

	if _, err := ReconcileBackends(proxyServer, service, []string{"local-stt"}); err == nil {
		t.Fatal("ReconcileBackends returned nil, want unsupported service error")
	}
	if backend := proxyServer.ListBackends()["local-stt"]; backend != nil {
		t.Fatalf("local-stt backend should not be registered for healthz service: %+v", backend)
	}
}

func TestRestoreOnlyRestoresReachableScanResults(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, svc := range []*state.ExternalService{
		{
			ID:         "external-reachable",
			BaseURL:    "http://127.0.0.1:8004",
			Kind:       "openai",
			Status:     "reachable",
			Source:     "scan",
			ModelsJSON: `["reachable-model"]`,
		},
		{
			ID:         "external-stale",
			BaseURL:    "http://127.0.0.1:8011",
			Kind:       "openai",
			Status:     "reachable",
			Source:     "scan",
			ModelsJSON: `["stale-model"]`,
		},
	} {
		if err := db.UpsertExternalService(ctx, svc); err != nil {
			t.Fatalf("UpsertExternalService: %v", err)
		}
		if err := db.SetExternalServiceImportedModels(ctx, svc.BaseURL, true, nil); err != nil {
			t.Fatalf("SetExternalServiceImportedModels: %v", err)
		}
	}

	proxyServer := proxy.NewServer()
	proxyServer.RegisterBackend("stale-model", &proxy.Backend{
		ModelName: "stale-model",
		Scheme:    "http",
		Address:   "127.0.0.1:8011",
		Ready:     true,
		External:  true,
	})
	proxyServer.RegisterBackend("local-model", &proxy.Backend{
		ModelName: "local-model",
		Address:   "10.42.0.8:8000",
		Ready:     true,
	})
	err = NewReconciler(db, proxyServer).Restore(ctx, map[string]struct{}{
		"http://127.0.0.1:8004": {},
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}

	backends := proxyServer.ListBackends()
	if backends["reachable-model"] == nil {
		t.Fatal("reachable-model backend missing")
	}
	if backends["stale-model"] != nil {
		t.Fatal("stale-model backend should not be restored")
	}
	if backends["local-model"] == nil {
		t.Fatal("local-model backend should not be removed by external stale cleanup")
	}
	services, err := db.ListExternalServices(ctx)
	if err != nil {
		t.Fatalf("ListExternalServices: %v", err)
	}
	for _, svc := range services {
		if svc.BaseURL == "http://127.0.0.1:8011" && svc.Status != "unreachable" {
			t.Fatalf("stale service status = %q, want unreachable", svc.Status)
		}
	}
}

func TestRestoreRemovesRoutesForModelsNoLongerAdvertised(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := &state.ExternalService{
		ID:         "external-reachable",
		BaseURL:    "http://127.0.0.1:8004",
		Kind:       "openai",
		Status:     "reachable",
		Source:     "scan",
		ModelsJSON: `["new-model"]`,
	}
	if err := db.UpsertExternalService(ctx, svc); err != nil {
		t.Fatalf("UpsertExternalService: %v", err)
	}
	if err := db.SetExternalServiceImportedModels(ctx, svc.BaseURL, true, []string{"old-model", "new-model"}); err != nil {
		t.Fatalf("SetExternalServiceImportedModels: %v", err)
	}

	proxyServer := proxy.NewServer()
	proxyServer.RegisterBackend("old-model", &proxy.Backend{
		ModelName: "old-model",
		Scheme:    "http",
		Address:   "127.0.0.1:8004",
		Ready:     true,
		External:  true,
	})

	if err := NewReconciler(db, proxyServer).Restore(ctx, map[string]struct{}{
		"http://127.0.0.1:8004": {},
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	backends := proxyServer.ListBackends()
	if backends["old-model"] != nil {
		t.Fatal("old-model backend should be removed after service stopped advertising it")
	}
	if backends["new-model"] == nil {
		t.Fatal("new-model backend should be registered")
	}
	services, err := db.ListExternalServices(ctx)
	if err != nil {
		t.Fatalf("ListExternalServices: %v", err)
	}
	if services[0].ImportedModelsJSON != `["new-model"]` {
		t.Fatalf("ImportedModelsJSON = %s, want pruned imported subset", services[0].ImportedModelsJSON)
	}
}

func TestRestoreKeepsImportedSubsetOnRestore(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	svc := &state.ExternalService{
		ID:         "external-reachable",
		BaseURL:    "http://127.0.0.1:8004",
		Kind:       "openai",
		Status:     "reachable",
		Source:     "scan",
		ModelsJSON: `["old-model","new-model"]`,
	}
	if err := db.UpsertExternalService(ctx, svc); err != nil {
		t.Fatalf("UpsertExternalService: %v", err)
	}
	if err := db.SetExternalServiceImportedModels(ctx, svc.BaseURL, true, []string{"new-model"}); err != nil {
		t.Fatalf("SetExternalServiceImportedModels: %v", err)
	}

	proxyServer := proxy.NewServer()
	if err := NewReconciler(db, proxyServer).Restore(ctx, map[string]struct{}{
		"http://127.0.0.1:8004": {},
	}); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	backends := proxyServer.ListBackends()
	if backends["old-model"] != nil {
		t.Fatal("old-model backend should not be restored when import selected only new-model")
	}
	if backends["new-model"] == nil {
		t.Fatal("new-model backend should be restored")
	}
}

func TestNormalizeHostPort(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"127.0.0.1:8080", "127.0.0.1:8080"},
		{"http://127.0.0.1:8080", "127.0.0.1:8080"},
		{"http://127.0.0.1:8080/v1", "127.0.0.1:8080"},
		{"localhost:8080", "127.0.0.1:8080"},
		{"http://localhost:8080", "127.0.0.1:8080"},
		{"0.0.0.0:8080", "127.0.0.1:8080"},
		{"10.42.0.8:8000", "10.42.0.8:8000"},
	}
	for _, c := range cases {
		if got := normalizeHostPort(c.in); got != c.want {
			t.Errorf("normalizeHostPort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// AIMA's own native deployment binds llama.cpp's default port 8080, which the
// external scanner also probes. List must not surface AIMA's own deployment
// backend as an importable external service; genuine external services stay.
func TestListExcludesOwnDeploymentServices(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open(:memory:): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	for _, svc := range []*state.ExternalService{
		{ID: "own-8080", BaseURL: "http://127.0.0.1:8080", Kind: "openai", Status: "reachable", Source: "scan", ModelsJSON: `["Qwen3.6-27B-Q4_K_M.gguf"]`},
		{ID: "real-ollama", BaseURL: "http://127.0.0.1:11434", Kind: "ollama", Status: "reachable", Source: "scan", ModelsJSON: `["llama3"]`},
	} {
		if err := db.UpsertExternalService(ctx, svc); err != nil {
			t.Fatalf("UpsertExternalService: %v", err)
		}
	}

	proxyServer := proxy.NewServer()
	// AIMA's own native deployment backend (External defaults to false).
	proxyServer.RegisterBackend("Qwen3.6-27B-Q4_K_M", &proxy.Backend{
		ModelName: "Qwen3.6-27B-Q4_K_M",
		Address:   "127.0.0.1:8080",
		Ready:     true,
	})

	services, err := NewReconciler(db, proxyServer).List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, svc := range services {
		if svc.BaseURL == "http://127.0.0.1:8080" {
			t.Errorf("List returned AIMA's own deployment as external service: %s", svc.BaseURL)
		}
	}
	var sawOllama bool
	for _, svc := range services {
		if svc.BaseURL == "http://127.0.0.1:11434" {
			sawOllama = true
		}
	}
	if !sawOllama {
		t.Errorf("genuine external service (ollama) was wrongly excluded; got %d services", len(services))
	}
}
