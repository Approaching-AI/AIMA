package openclaw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectConnected(t *testing.T) {
	t.Parallel()

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer gateway.Close()

	u, err := url.Parse(gateway.URL)
	if err != nil {
		t.Fatalf("parse gateway url: %v", err)
	}

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	logPath := filepath.Join(tmpDir, "logs", "gateway.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		t.Fatalf("mkdir logs: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("2026-03-31 [gateway] listening on ws://127.0.0.1:"+u.Port()+", ws://[::1]:"+u.Port()+"\n"), 0644); err != nil {
		t.Fatalf("write gateway log: %v", err)
	}

	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b":       {ModelName: "qwen3-8b", Address: "127.0.0.1:8000", Ready: true},
			"glm-4.1v-9b":    {ModelName: "glm-4.1v-9b", Address: "127.0.0.1:8001", Ready: true},
			"qwen3-asr-1.7b": {ModelName: "qwen3-asr-1.7b", Address: "127.0.0.1:8002", Ready: true},
			"qwen3-tts-0.6b": {ModelName: "qwen3-tts-0.6b", Address: "127.0.0.1:8003", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	status, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !status.Connected {
		t.Fatalf("connected = false, issues=%v", status.Issues)
	}
	if !status.GatewayLive {
		t.Fatal("gateway_live = false, want true")
	}
	if !status.AIMAConfigured {
		t.Fatal("aima_configured = false, want true")
	}
	if !status.SyncReady {
		t.Fatalf("sync_ready = false, expected=%+v configured=%+v", status.Expected, status.Configured)
	}
	if status.ClaimNeeded {
		t.Fatal("claim_needed = true, want false")
	}
	if got, want := strings.Join(status.Expected.ChatModels, ","), "qwen3-8b"; got != want {
		t.Fatalf("expected chat models = %q, want %q", got, want)
	}
	if got, want := strings.Join(status.Expected.VisionModels, ","), "glm-4.1v-9b"; got != want {
		t.Fatalf("expected vision models = %q, want %q", got, want)
	}
	if got := status.Configured.TTSModel; got != "qwen3-tts-0.6b" {
		t.Fatalf("configured tts = %q, want qwen3-tts-0.6b", got)
	}
}

func TestInspectTracksImageGenConfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"z-image": {ModelName: "z-image", Address: "127.0.0.1:8188", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	status, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if len(status.Expected.ImageGenModels) != 1 || status.Expected.ImageGenModels[0] != "z-image" {
		t.Fatalf("expected image_gen_models = %+v, want [z-image]", status.Expected.ImageGenModels)
	}
	if len(status.Configured.ImageGenModels) != 1 || status.Configured.ImageGenModels[0] != "z-image" {
		t.Fatalf("configured image_gen_models = %+v, want [z-image]", status.Configured.ImageGenModels)
	}
	if !status.AIMAConfigured {
		t.Fatal("aima_configured = false, want true")
	}
	if !status.SyncReady {
		t.Fatalf("sync_ready = false, expected=%+v configured=%+v issues=%v", status.Expected, status.Configured, status.Issues)
	}
	if status.ClaimNeeded {
		t.Fatal("claim_needed = true, want false")
	}
	for _, issue := range status.Issues {
		if strings.Contains(issue, "image generation models are detected") {
			t.Fatalf("issues = %v, should not report image generation as unsupported", status.Issues)
		}
	}
}

func TestInspectReportsClaimNeeded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := writeLegacyClaimableConfig(t, tmpDir, "http://127.0.0.1:6188/v1")
	deps := legacyClaimDeps(configPath)

	status, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if !status.ClaimNeeded {
		t.Fatalf("claim_needed = false, issues=%v", status.Issues)
	}
	if status.Connected {
		t.Fatalf("connected = true, want false when claim is still needed")
	}
	if got := status.Claimable.TTSModel; got != "qwen3-tts-0.6b" {
		t.Fatalf("claimable tts = %q, want qwen3-tts-0.6b", got)
	}
	found := false
	for _, issue := range status.Issues {
		if strings.Contains(issue, "action=claim") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %v, want claim guidance", status.Issues)
	}
}

func TestInspectIgnoresUnexpectedLocalProxyConfig(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := writeLegacyClaimableConfig(t, tmpDir, "http://127.0.0.1:6188/v1")
	deps := &Deps{
		Backends:   &mockBackends{backends: map[string]*Backend{}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	status, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if status.ClaimNeeded {
		t.Fatalf("claim_needed = true, want false when AIMA does not currently expect these models")
	}
	if summaryCount(status.Claimable) != 0 {
		t.Fatalf("claimable = %+v, want empty", status.Claimable)
	}
}

func TestInspectReportsMCPServer(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b": {ModelName: "qwen3-8b", Address: "127.0.0.1:8000", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("sync: %v", err)
	}

	status, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if status.MCPServer == nil {
		t.Fatal("mcp_server = nil, want populated")
	}
	if !status.MCPServer.Registered || !status.MCPServer.Managed {
		t.Fatalf("mcp_server = %+v, want registered managed entry", status.MCPServer)
	}
	if !status.SyncReady {
		t.Fatalf("sync_ready = false, issues=%v", status.Issues)
	}
}

func TestInspectReportsMissingManagedPluginAssets(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-asr-1.7b": {ModelName: "qwen3-asr-1.7b", Address: "127.0.0.1:8002", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if err := os.Remove(filepath.Join(tmpDir, "extensions", "aima-local-audio", "index.js")); err != nil {
		t.Fatalf("remove plugin asset: %v", err)
	}

	status, err := Inspect(context.Background(), deps)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	if status.SyncReady {
		t.Fatalf("sync_ready = true, want false when managed plugin assets are missing; issues=%v", status.Issues)
	}
	found := false
	for _, issue := range status.Issues {
		if strings.Contains(issue, `AIMA-managed plugin "aima-local-audio" is missing deployed assets`) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("issues = %v, want missing plugin asset issue", status.Issues)
	}
}
