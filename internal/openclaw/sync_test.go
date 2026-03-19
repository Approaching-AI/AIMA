package openclaw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type mockBackends struct {
	backends map[string]*Backend
}

func (m *mockBackends) ListBackends() map[string]*Backend { return m.backends }

type mockCatalog struct{}

func (m *mockCatalog) ModelType(name string) string {
	switch name {
	case "qwen3-8b":
		return "llm"
	case "glm-4.1v-9b":
		return "vlm"
	case "qwen3-asr-1.7b":
		return "asr"
	case "qwen3-tts-0.6b":
		return "tts"
	default:
		return ""
	}
}

func (m *mockCatalog) ModelContextWindow(name string) int {
	switch name {
	case "qwen3-8b":
		return 32768
	case "glm-4.1v-9b":
		return 8192
	default:
		return 0
	}
}

func (m *mockCatalog) ModelFamily(name string) string {
	switch name {
	case "qwen3-8b", "qwen3-asr-1.7b", "qwen3-tts-0.6b":
		return "qwen"
	case "glm-4.1v-9b":
		return "glm"
	default:
		return ""
	}
}

func TestSyncDryRun(t *testing.T) {
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b":       {ModelName: "qwen3-8b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
			"qwen3-asr-1.7b": {ModelName: "qwen3-asr-1.7b", EngineType: "qwen-asr-fastapi", Address: "http://127.0.0.1:8001", Ready: true},
			"qwen3-tts-0.6b": {ModelName: "qwen3-tts-0.6b", EngineType: "qwen-tts-fastapi", Address: "http://127.0.0.1:8002", Ready: true},
			"not-ready":      {ModelName: "not-ready", EngineType: "vllm", Address: "http://127.0.0.1:8003", Ready: false},
			"remote-model":   {ModelName: "remote-model", EngineType: "vllm", Address: "http://192.168.1.5:8000", Ready: true, Remote: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: "/tmp/test-openclaw.json",
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	result, err := Sync(context.Background(), deps, true)
	if err != nil {
		t.Fatalf("Sync dry-run failed: %v", err)
	}

	if result.Written {
		t.Error("dry-run should not write")
	}
	if len(result.LLMModels) != 1 {
		t.Errorf("expected 1 LLM model, got %d", len(result.LLMModels))
	}
	if result.LLMModels[0].ID != "qwen3-8b" {
		t.Errorf("expected LLM model qwen3-8b, got %s", result.LLMModels[0].ID)
	}
	if len(result.LLMModels[0].Input) != 1 || result.LLMModels[0].Input[0] != "text" {
		t.Errorf("expected LLM input [text], got %v", result.LLMModels[0].Input)
	}
	if len(result.ASRModels) != 1 {
		t.Errorf("expected 1 ASR model, got %d", len(result.ASRModels))
	}
	if result.TTSModel == nil || result.TTSModel.ID != "qwen3-tts-0.6b" {
		t.Errorf("expected TTS model qwen3-tts-0.6b, got %v", result.TTSModel)
	}
}

func TestSyncWritesConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")

	// Pre-populate with existing config (simulating minimax provider)
	existing := map[string]any{
		"models": map[string]any{
			"providers": map[string]any{
				"minimax": map[string]any{
					"baseUrl": "https://api.minimaxi.com",
					"models":  []any{map[string]any{"id": "MiniMax-M2.1"}},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(configPath, data, 0644)

	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b": {ModelName: "qwen3-8b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		APIKey:     "test-key",
	}

	result, err := Sync(context.Background(), deps, false)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if !result.Written {
		t.Error("expected Written=true")
	}

	// Read back and verify
	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}

	// Verify minimax provider is preserved
	models := cfg["models"].(map[string]any)
	providers := models["providers"].(map[string]any)
	if _, ok := providers["minimax"]; !ok {
		t.Error("minimax provider was removed — merge should preserve non-AIMA providers")
	}

	// Verify vllm provider was added
	vllm, ok := providers["vllm"].(map[string]any)
	if !ok {
		t.Fatal("vllm provider not found after sync")
	}
	if vllm["baseUrl"] != "http://127.0.0.1:6188/v1" {
		t.Errorf("vllm baseUrl = %v, want http://127.0.0.1:6188/v1", vllm["baseUrl"])
	}
	if vllm["apiKey"] != "test-key" {
		t.Errorf("vllm apiKey = %v, want test-key", vllm["apiKey"])
	}
}

func TestSyncVLMInput(t *testing.T) {
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"glm-4.1v-9b": {ModelName: "glm-4.1v-9b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: "/tmp/test-openclaw.json",
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	result, err := Sync(context.Background(), deps, true)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if len(result.LLMModels) != 1 {
		t.Fatalf("expected 1 LLM model, got %d", len(result.LLMModels))
	}
	// VLM models should have ["text", "image"] input
	if len(result.LLMModels[0].Input) != 2 || result.LLMModels[0].Input[1] != "image" {
		t.Errorf("VLM input should be [text, image], got %v", result.LLMModels[0].Input)
	}
}

func TestFormatDisplayName(t *testing.T) {
	tests := []struct {
		model, typ, want string
	}{
		{"qwen3-8b", "llm", "Qwen3 8B (AIMA)"},
		{"glm-4.1v-9b", "vlm", "Glm 4.1v 9B (AIMA VLM)"},
		{"qwen3-tts-0.6b", "tts", "Qwen3 Tts 0.6B (AIMA)"},
	}
	for _, tt := range tests {
		got := formatDisplayName(tt.model, tt.typ)
		if got != tt.want {
			t.Errorf("formatDisplayName(%q, %q) = %q, want %q", tt.model, tt.typ, got, tt.want)
		}
	}
}

func TestDefaultMaxTokens(t *testing.T) {
	tests := []struct {
		ctx, want int
	}{
		{0, 4096},
		{2048, 1024},
		{32768, 8192},
		{65536, 8192},
		{8192, 2048},
	}
	for _, tt := range tests {
		got := defaultMaxTokens(tt.ctx)
		if got != tt.want {
			t.Errorf("defaultMaxTokens(%d) = %d, want %d", tt.ctx, got, tt.want)
		}
	}
}
