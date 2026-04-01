package openclaw

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	case "z-image":
		return "image_gen"
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
	case "z-image":
		return "tongyi"
	default:
		return ""
	}
}

func (m *mockCatalog) OpenClawRequestPatches(name string) []RequestPatch {
	if name != "qwen3.5-9b" {
		return nil
	}
	return []RequestPatch{{
		Path:           "/v1/chat/completions",
		EnginePrefixes: []string{"vllm"},
		Body: map[string]any{
			"chat_template_kwargs": map[string]any{
				"enable_thinking": false,
			},
		},
	}}
}

func TestSyncDryRun(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b":       {ModelName: "qwen3-8b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
			"qwen3-asr-1.7b": {ModelName: "qwen3-asr-1.7b", EngineType: "qwen-asr-fastapi", Address: "http://127.0.0.1:8001", Ready: true},
			"qwen3-tts-0.6b": {ModelName: "qwen3-tts-0.6b", EngineType: "qwen-tts-fastapi", Address: "http://127.0.0.1:8002", Ready: true},
			"not-ready":      {ModelName: "not-ready", EngineType: "vllm", Address: "http://127.0.0.1:8003", Ready: false},
			"remote-model":   {ModelName: "remote-model", EngineType: "vllm", Address: "http://192.168.1.5:8000", Ready: true, Remote: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
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
	if result.MCPServer == nil {
		t.Fatal("expected MCP server state")
	}
	if !result.MCPServer.Registered || !result.MCPServer.Managed {
		t.Fatalf("expected managed MCP server, got %+v", result.MCPServer)
	}
	if result.MCPServer.Action != "managed" {
		t.Fatalf("mcp action = %q, want managed", result.MCPServer.Action)
	}
	if result.ConfigExists {
		t.Fatal("configExists = true, want false")
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
		APIKey:     func() string { return "test-key" },
		MCPCommand: "/usr/local/bin/aima",
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

	// Verify AIMA provider was added
	aima, ok := providers["aima"].(map[string]any)
	if !ok {
		t.Fatal("aima provider not found after sync")
	}
	if aima["baseUrl"] != "http://127.0.0.1:6188/v1" {
		t.Errorf("aima baseUrl = %v, want http://127.0.0.1:6188/v1", aima["baseUrl"])
	}
	if aima["apiKey"] != "test-key" {
		t.Errorf("aima apiKey = %v, want test-key", aima["apiKey"])
	}

	managed, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("ReadManagedState failed: %v", err)
	}
	if managed.LLMProvider != "aima" {
		t.Fatalf("managed llm provider = %q, want aima", managed.LLMProvider)
	}
	if managed.MCPServerName != "aima" {
		t.Fatalf("managed mcp server = %q, want aima", managed.MCPServerName)
	}
	server := lookupMap(cfg, "mcp", "servers", "aima")
	if server == nil {
		t.Fatal("mcp.servers.aima missing after sync")
	}
	if got := server["command"]; got != "/usr/local/bin/aima" {
		t.Fatalf("mcp.servers.aima.command = %v, want /usr/local/bin/aima", got)
	}
	if got := stringArgs(server["args"]); len(got) != 3 || got[0] != "mcp" || got[1] != "--profile" || got[2] != "operator" {
		t.Fatalf("mcp.servers.aima.args = %v, want [mcp --profile operator]", got)
	}
}

func TestSyncCreatesMissingConfigDirectory(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "nested", "openclaw", "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b": {ModelName: "qwen3-8b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}

	result, err := Sync(context.Background(), deps, false)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if !result.Written {
		t.Fatal("expected Written=true for missing config directory")
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected config to be written: %v", err)
	}
	if _, err := os.Stat(ManagedStatePath(configPath)); err != nil {
		t.Fatalf("expected managed state to be written: %v", err)
	}
}

func TestSyncWritesTTSProviderSchema(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-tts-0.6b": {ModelName: "qwen3-tts-0.6b", EngineType: "qwen-tts-fastapi", Address: "http://127.0.0.1:8003", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	tts := lookupMap(cfg, "messages", "tts")
	if tts == nil {
		t.Fatal("messages.tts missing after sync")
	}
	if _, ok := tts["openai"]; ok {
		t.Fatalf("messages.tts.openai should not be written anymore: %v", tts)
	}
	openaiTTS := lookupMap(cfg, "messages", "tts", "providers", "openai")
	if openaiTTS == nil {
		t.Fatal("messages.tts.providers.openai missing after sync")
	}
	if got := openaiTTS["model"]; got != "qwen3-tts-0.6b" {
		t.Fatalf("messages.tts.providers.openai.model = %v, want qwen3-tts-0.6b", got)
	}
}

func TestSyncWritesLocalMediaProviderForASR(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-asr-1.7b": {ModelName: "qwen3-asr-1.7b", EngineType: "qwen-asr-fastapi", Address: "http://127.0.0.1:8001", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	provider := lookupMap(cfg, "models", "providers", "aima-media")
	if provider == nil {
		t.Fatal("models.providers.aima-media missing after ASR sync")
	}
	if got := provider["baseUrl"]; got != "http://127.0.0.1:6188/v1" {
		t.Fatalf("provider baseUrl = %v, want http://127.0.0.1:6188/v1", got)
	}
	if got := provider["apiKey"]; got != "local" {
		t.Fatalf("provider apiKey = %v, want local", got)
	}
	models, ok := provider["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("provider models = %#v, want 1 media model", provider["models"])
	}
	model, ok := models[0].(map[string]any)
	if !ok {
		t.Fatalf("provider model[0] = %T, want map", models[0])
	}
	if got := model["id"]; got != "qwen3-asr-1.7b" {
		t.Fatalf("provider model[0].id = %v, want qwen3-asr-1.7b", got)
	}
	if got := stringArgs(model["input"]); len(got) != 1 || got[0] != "text" {
		t.Fatalf("provider model[0].input = %v, want [text]", got)
	}

	managed, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("ReadManagedState failed: %v", err)
	}
	if managed.MediaProvider != "aima-media" {
		t.Fatalf("managed media provider = %q, want aima-media", managed.MediaProvider)
	}
}

func TestSyncWritesImageModelForVLM(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"glm-4.1v-9b": {ModelName: "glm-4.1v-9b", EngineType: "vllm", Address: "http://127.0.0.1:8004", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	defaults := lookupMap(cfg, "agents", "defaults")
	if defaults == nil {
		t.Fatal("agents.defaults missing after sync")
	}
	imageModel, ok := defaults["imageModel"].(map[string]any)
	if !ok {
		t.Fatalf("imageModel = %T, want map", defaults["imageModel"])
	}
	if got := imageModel["primary"]; got != "aima/glm-4.1v-9b" {
		t.Fatalf("imageModel.primary = %v, want aima/glm-4.1v-9b", got)
	}

	managed, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("ReadManagedState failed: %v", err)
	}
	if managed.ImageModelProvider != "aima" {
		t.Fatalf("managed image model provider = %q, want aima", managed.ImageModelProvider)
	}
	if len(managed.ImageModelModels) != 1 || managed.ImageModelModels[0] != "glm-4.1v-9b" {
		t.Fatalf("managed image model models = %v, want [glm-4.1v-9b]", managed.ImageModelModels)
	}
}

func TestMergeAIMAConfigImageGenUsesAIMAProvider(t *testing.T) {
	merged := MergeAIMAConfig(nil, &SyncResult{
		ImageGenModels: []ImageGenEntry{{ID: "z-image"}},
		ProxyAddr:      "http://127.0.0.1:6188/v1",
	})

	provider := lookupMap(merged, "models", "providers", "aima-imagegen")
	if provider == nil {
		t.Fatal("aima-imagegen provider not found after image generation merge")
	}
	if got := provider["baseUrl"]; got != "http://127.0.0.1:6188/v1" {
		t.Fatalf("provider baseUrl = %v, want http://127.0.0.1:6188/v1", got)
	}
	if got := provider["apiKey"]; got != "local" {
		t.Fatalf("provider apiKey = %v, want local", got)
	}
	models, ok := provider["models"].([]any)
	if !ok {
		t.Fatalf("aima-imagegen provider models = %T, want []any", provider["models"])
	}
	if len(models) != 0 {
		t.Fatalf("aima-imagegen provider should expose an empty model catalog, got %v", models)
	}
	defaults := lookupMap(merged, "agents", "defaults")
	if defaults == nil {
		t.Fatal("agents.defaults missing after image generation merge")
	}
	imageGen, ok := defaults["imageGenerationModel"].(map[string]any)
	if !ok {
		t.Fatalf("imageGenerationModel = %T, want map", defaults["imageGenerationModel"])
	}
	if got := imageGen["primary"]; got != "aima-imagegen/z-image" {
		t.Fatalf("imageGenerationModel.primary = %v, want aima-imagegen/z-image", got)
	}
}

func TestSyncMigratesLegacyImageGenProvider(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := writeLegacyClaimableConfig(t, tmpDir, "http://127.0.0.1:6188/v1")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"z-image": {ModelName: "z-image", Address: "127.0.0.1:8188", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if provider := lookupMap(cfg, "models", "providers", "openai"); provider != nil {
		t.Fatalf("legacy openai provider should be migrated away: %v", provider)
	}
	provider := lookupMap(cfg, "models", "providers", "aima-imagegen")
	if provider == nil {
		t.Fatal("aima-imagegen provider missing after sync")
	}
	models, ok := provider["models"].([]any)
	if !ok {
		t.Fatalf("aima-imagegen provider models = %T, want []any", provider["models"])
	}
	if len(models) != 0 {
		t.Fatalf("aima-imagegen provider should expose an empty model catalog, got %v", models)
	}
	defaults := lookupMap(cfg, "agents", "defaults")
	if defaults == nil {
		t.Fatal("agents.defaults missing after sync")
	}
	imageGen, ok := defaults["imageGenerationModel"].(map[string]any)
	if !ok {
		t.Fatalf("imageGenerationModel = %T, want map", defaults["imageGenerationModel"])
	}
	if got := imageGen["primary"]; got != "aima-imagegen/z-image" {
		t.Fatalf("imageGenerationModel.primary = %v, want aima-imagegen/z-image", got)
	}

	managed, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("ReadManagedState failed: %v", err)
	}
	if managed.ImageGenerationProvider != "aima-imagegen" {
		t.Fatalf("managed image generation provider = %q, want aima-imagegen", managed.ImageGenerationProvider)
	}
}

func TestSyncMigratesLegacyImageGenProviderWithMedia(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := writeLegacyClaimableConfig(t, tmpDir, "http://127.0.0.1:6188/v1")
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"z-image":        {ModelName: "z-image", Address: "127.0.0.1:8188", Ready: true},
			"qwen3-asr-1.7b": {ModelName: "qwen3-asr-1.7b", Address: "127.0.0.1:8003", Ready: true},
			"glm-4.1v-9b":    {ModelName: "glm-4.1v-9b", Address: "127.0.0.1:8004", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}

	if _, err := Sync(context.Background(), deps, false); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if provider := lookupMap(cfg, "models", "providers", "openai"); provider != nil {
		t.Fatalf("legacy openai provider should be migrated away: %v", provider)
	}
	if provider := lookupMap(cfg, "models", "providers", "aima-imagegen"); provider == nil {
		t.Fatal("aima-imagegen provider missing after sync")
	}
	if provider := lookupMap(cfg, "models", "providers", "aima-media"); provider == nil {
		t.Fatal("aima-media provider missing after sync")
	}
	audio := mediaModels(lookupMap(cfg, "tools", "media", "audio"), "http://127.0.0.1:6188/v1")
	if len(audio) != 1 || audio[0] != "qwen3-asr-1.7b" {
		t.Fatalf("audio models = %v, want [qwen3-asr-1.7b]", audio)
	}
	vision := mediaModels(lookupMap(cfg, "tools", "media", "image"), "http://127.0.0.1:6188/v1")
	if len(vision) != 1 || vision[0] != "glm-4.1v-9b" {
		t.Fatalf("vision models = %v, want [glm-4.1v-9b]", vision)
	}

	managed, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("ReadManagedState failed: %v", err)
	}
	if managed.ImageGenerationProvider != "aima-imagegen" {
		t.Fatalf("managed image generation provider = %q, want aima-imagegen", managed.ImageGenerationProvider)
	}
	if len(managed.AudioModels) != 1 || managed.AudioModels[0] != "qwen3-asr-1.7b" {
		t.Fatalf("managed audio models = %v, want [qwen3-asr-1.7b]", managed.AudioModels)
	}
	if len(managed.VisionModels) != 1 || managed.VisionModels[0] != "glm-4.1v-9b" {
		t.Fatalf("managed vision models = %v, want [glm-4.1v-9b]", managed.VisionModels)
	}
}

func TestMergeAIMAConfigPreservesUnownedSharedSections(t *testing.T) {
	proxyAddr := "http://127.0.0.1:6188/v1"
	existing := map[string]any{
		"models": map[string]any{
			"providers": map[string]any{
				"aima": map[string]any{
					"baseUrl": proxyAddr,
					"api":     "openai-completions",
					"models":  []any{map[string]any{"id": "qwen3-8b"}},
				},
				"vllm": map[string]any{
					"baseUrl": proxyAddr,
					"api":     "openai-completions",
					"models":  []any{map[string]any{"id": "qwen3-8b"}},
				},
				"openai": map[string]any{
					"baseUrl": proxyAddr,
					"api":     "openai-completions",
					"models":  []any{map[string]any{"id": "z-image"}},
				},
				"minimax": map[string]any{
					"baseUrl": "https://api.minimax.chat/v1",
					"models":  []any{map[string]any{"id": "MiniMax-M2.1"}},
				},
			},
		},
		"tools": map[string]any{
			"media": map[string]any{
				"audio": map[string]any{
					"enabled": true,
					"models": []any{
						map[string]any{"provider": "openai", "model": "qwen3-asr-1.7b", "baseUrl": proxyAddr},
						map[string]any{"provider": "openai", "model": "whisper-1", "baseUrl": "https://api.openai.com/v1"},
					},
				},
				"image": map[string]any{
					"enabled": true,
					"models": []any{
						map[string]any{"provider": "openai", "model": "glm-4.1v-9b", "baseUrl": proxyAddr},
					},
				},
			},
		},
		"messages": map[string]any{
			"tts": map[string]any{
				"provider": "openai",
				"openai": map[string]any{
					"baseUrl": proxyAddr,
					"model":   "qwen3-tts-0.6b",
					"voice":   "default",
				},
			},
		},
		"env": map[string]any{
			"OPENAI_TTS_BASE_URL": proxyAddr,
		},
		"agents": map[string]any{
			"defaults": map[string]any{
				"imageModel": map[string]any{
					"primary": "minimax/MiniMax-VL-01",
				},
				"imageGenerationModel": map[string]any{
					"primary": "openai/z-image",
				},
			},
		},
	}

	merged := MergeAIMAConfig(existing, &SyncResult{ProxyAddr: proxyAddr})
	providers := lookupMap(merged, "models", "providers")
	if providers == nil {
		t.Fatal("models.providers missing after merge")
	}
	if _, ok := providers["minimax"]; !ok {
		t.Fatal("minimax provider should be preserved")
	}
	if _, ok := providers["aima"]; ok {
		t.Fatal("stale aima provider should be removed")
	}
	if _, ok := providers["vllm"]; ok {
		t.Fatal("stale legacy vllm provider should be removed")
	}
	if _, ok := providers["openai"]; !ok {
		t.Fatal("shared openai provider should be preserved without explicit ownership")
	}

	audio := lookupMap(merged, "tools", "media", "audio")
	if audio == nil {
		t.Fatal("tools.media.audio should preserve non-AIMA models")
	}
	audioModels := mediaModels(audio, "https://api.openai.com/v1")
	if len(audioModels) != 1 || audioModels[0] != "whisper-1" {
		t.Fatalf("preserved audio models = %v, want [whisper-1]", audioModels)
	}
	if image := lookupMap(merged, "tools", "media", "image"); image == nil {
		t.Fatal("tools.media.image should be preserved without explicit ownership")
	}
	if messages := lookupMap(merged, "messages"); messages == nil {
		t.Fatal("messages.tts should be preserved without explicit ownership")
	}
	if env := lookupMap(merged, "env"); env == nil {
		t.Fatal("env should be preserved without explicit ownership")
	}
	if defaults := lookupMap(merged, "agents", "defaults"); defaults != nil {
		if got := lookupMap(merged, "agents", "defaults")["imageModel"]; got == nil {
			t.Fatal("imageModel should be preserved without explicit ownership")
		}
		if _, ok := defaults["imageGenerationModel"]; !ok {
			t.Fatal("imageGenerationModel should be preserved without explicit ownership")
		}
	}
}

func TestMergeAIMAConfigWithManagedStateRemovesOwnedSharedSections(t *testing.T) {
	proxyAddr := "http://127.0.0.1:6188/v1"
	existing := map[string]any{
		"models": map[string]any{
			"providers": map[string]any{
				"openai": map[string]any{
					"baseUrl": proxyAddr,
					"api":     "openai-completions",
					"models":  []any{map[string]any{"id": "z-image"}},
				},
			},
		},
		"tools": map[string]any{
			"media": map[string]any{
				"audio": map[string]any{
					"enabled": true,
					"models": []any{
						map[string]any{"provider": "openai", "model": "qwen3-asr-1.7b", "baseUrl": "https://example.invalid/v1"},
						map[string]any{"provider": "openai", "model": "whisper-1", "baseUrl": "https://api.openai.com/v1"},
					},
				},
				"image": map[string]any{
					"enabled": true,
					"models": []any{
						map[string]any{"provider": "openai", "model": "glm-4.1v-9b", "baseUrl": "https://example.invalid/v1"},
					},
				},
			},
		},
		"messages": map[string]any{
			"tts": map[string]any{
				"provider": "openai",
				"openai": map[string]any{
					"baseUrl": "https://example.invalid/v1",
					"model":   "qwen3-tts-0.6b",
					"voice":   "default",
				},
			},
		},
		"env": map[string]any{
			"OPENAI_TTS_BASE_URL": "https://example.invalid/v1",
		},
		"agents": map[string]any{
			"defaults": map[string]any{
				"imageModel": map[string]any{
					"primary": "openai/glm-4.1v-9b",
				},
				"imageGenerationModel": map[string]any{
					"primary": "openai/z-image",
				},
			},
		},
	}

	managed := &ManagedState{
		Version:                 managedStateVersion,
		MediaProvider:           "openai",
		AudioModels:             []string{"qwen3-asr-1.7b"},
		VisionModels:            []string{"glm-4.1v-9b"},
		ImageModelProvider:      "openai",
		ImageModelModels:        []string{"glm-4.1v-9b"},
		TTSModel:                "qwen3-tts-0.6b",
		ImageGenerationProvider: "openai",
		ImageGenerationModels:   []string{"z-image"},
	}

	merged, next := MergeAIMAConfigWithState(existing, managed, &SyncResult{ProxyAddr: proxyAddr})
	if next.Empty() == false {
		t.Fatalf("next managed state should be empty after removing all managed sections: %+v", next)
	}
	if providers := lookupMap(merged, "models", "providers"); providers != nil {
		if _, ok := providers["openai"]; ok {
			t.Fatal("managed openai provider should be removed")
		}
	}
	audio := lookupMap(merged, "tools", "media", "audio")
	if audio == nil {
		t.Fatal("audio section should preserve unmanaged models")
	}
	audioModels := mediaModels(audio, "https://api.openai.com/v1")
	if len(audioModels) != 1 || audioModels[0] != "whisper-1" {
		t.Fatalf("preserved audio models = %v, want [whisper-1]", audioModels)
	}
	if image := lookupMap(merged, "tools", "media", "image"); image != nil {
		t.Fatalf("managed image media should be removed: %v", image)
	}
	if messages := lookupMap(merged, "messages"); messages != nil {
		t.Fatalf("managed messages.tts should be removed: %v", messages)
	}
	if env := lookupMap(merged, "env"); env != nil {
		t.Fatalf("managed env should be removed: %v", env)
	}
	if defaults := lookupMap(merged, "agents", "defaults"); defaults != nil {
		if _, ok := defaults["imageModel"]; ok {
			t.Fatal("managed imageModel should be removed")
		}
		if _, ok := defaults["imageGenerationModel"]; ok {
			t.Fatal("managed imageGenerationModel should be removed")
		}
	}
}

func TestSyncVLMInput(t *testing.T) {
	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"glm-4.1v-9b": {ModelName: "glm-4.1v-9b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: filepath.Join(t.TempDir(), "openclaw.json"),
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
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

func TestSyncPreservesUnmanagedMCPServer(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	existing := map[string]any{
		"mcp": map[string]any{
			"servers": map[string]any{
				"aima": map[string]any{
					"command": "custom-aima",
					"args":    []any{"mcp", "--profile", "explorer"},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	deps := &Deps{
		Backends: &mockBackends{backends: map[string]*Backend{
			"qwen3-8b": {ModelName: "qwen3-8b", EngineType: "vllm", Address: "http://127.0.0.1:8000", Ready: true},
		}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}

	result, err := Sync(context.Background(), deps, false)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if result.MCPServer == nil {
		t.Fatal("expected MCP server state")
	}
	if result.MCPServer.Action != "preserved_unmanaged" {
		t.Fatalf("mcp action = %q, want preserved_unmanaged", result.MCPServer.Action)
	}
	if result.MCPServer.Managed {
		t.Fatalf("expected unmanaged MCP server, got %+v", result.MCPServer)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	server := lookupMap(cfg, "mcp", "servers", "aima")
	if server == nil {
		t.Fatal("expected preserved unmanaged mcp.servers.aima")
	}
	if got := server["command"]; got != "custom-aima" {
		t.Fatalf("command = %v, want custom-aima", got)
	}

	managed, err := ReadManagedState(configPath)
	if err != nil {
		t.Fatalf("ReadManagedState failed: %v", err)
	}
	if managed.MCPServerName != "" {
		t.Fatalf("managed mcp server = %q, want empty", managed.MCPServerName)
	}
}

func TestDeploySkillsOnlyWritesControlSkill(t *testing.T) {
	t.Parallel()

	targetDir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(targetDir, "aima-asr"), 0755); err != nil {
		t.Fatalf("mkdir stale skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "aima-asr", "SKILL.md"), []byte("stale"), 0644); err != nil {
		t.Fatalf("write stale skill: %v", err)
	}
	if err := DeploySkills(targetDir); err != nil {
		t.Fatalf("DeploySkills failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, "aima-control", "SKILL.md")); err != nil {
		t.Fatalf("expected aima-control skill: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "aima-asr")); !os.IsNotExist(err) {
		t.Fatalf("aima-asr should not be deployed by default, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "aima-tts")); !os.IsNotExist(err) {
		t.Fatalf("aima-tts should not be deployed by default, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "aima-image-gen")); !os.IsNotExist(err) {
		t.Fatalf("aima-image-gen should not be deployed by default, stat err=%v", err)
	}
}

func TestDeployPluginsWritesAIMALocalImagePlugin(t *testing.T) {
	t.Parallel()

	targetDir := filepath.Join(t.TempDir(), "extensions")
	if err := DeployPlugins(targetDir); err != nil {
		t.Fatalf("DeployPlugins failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(targetDir, "aima-local-image", "openclaw.plugin.json")); err != nil {
		t.Fatalf("expected plugin manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "aima-local-image", "package.json")); err != nil {
		t.Fatalf("expected plugin package.json: %v", err)
	}
	entryPath := filepath.Join(targetDir, "aima-local-image", "index.js")
	if _, err := os.Stat(entryPath); err != nil {
		t.Fatalf("expected plugin entrypoint: %v", err)
	}
	entryData, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatalf("ReadFile(plugin entrypoint): %v", err)
	}
	if !strings.Contains(string(entryData), `api.on("tool_result_persist"`) {
		t.Fatal("expected image plugin to register tool_result_persist hook")
	}
	if !strings.Contains(string(entryData), `api.on("before_tool_call"`) {
		t.Fatal("expected image plugin to register before_tool_call hook")
	}
	if !strings.Contains(string(entryData), `api.on("after_tool_call"`) {
		t.Fatal("expected image plugin to register after_tool_call hook")
	}
	if !strings.Contains(string(entryData), `Do not use placeholder paths like /tmp/tmp.jpg or /path/to/generated-image.jpg.`) {
		t.Fatal("expected image plugin guidance to forbid placeholder image paths")
	}
	if !strings.Contains(string(entryData), `".openclaw", "workspace", "media"`) {
		t.Fatal("expected image plugin to copy generated images into the OpenClaw workspace")
	}
	if !strings.Contains(string(entryData), `const rememberedGeneratedImages = new Map();`) {
		t.Fatal("expected image plugin to remember image_generate outputs for later image tool calls")
	}
	if !strings.Contains(string(entryData), `".openclaw", "state", "aima-local-image.json"`) {
		t.Fatal("expected image plugin to persist generated image references across hook contexts")
	}
}


func TestSyncKeepsManagedMCPServerWithoutReadyBackends(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "openclaw.json")
	existing := map[string]any{
		"models": map[string]any{
			"providers": map[string]any{
				"aima": map[string]any{
					"baseUrl": "http://127.0.0.1:6188/v1",
					"models":  []any{map[string]any{"id": "qwen3-8b"}},
				},
			},
		},
		"mcp": map[string]any{
			"servers": map[string]any{
				"aima": map[string]any{
					"command": "/usr/local/bin/aima",
					"args":    []any{"mcp", "--profile", "operator"},
				},
			},
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := WriteManagedState(configPath, &ManagedState{
		Version:       managedStateVersion,
		LLMProvider:   "aima",
		MCPServerName: "aima",
	}); err != nil {
		t.Fatalf("WriteManagedState: %v", err)
	}

	deps := &Deps{
		Backends:   &mockBackends{backends: map[string]*Backend{}},
		Catalog:    &mockCatalog{},
		ConfigPath: configPath,
		ProxyAddr:  "http://127.0.0.1:6188/v1",
		MCPCommand: "/usr/local/bin/aima",
	}

	result, err := Sync(context.Background(), deps, false)
	if err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
	if result.MCPServer == nil || !result.MCPServer.Managed || !result.MCPServer.Registered {
		t.Fatalf("expected managed MCP server, got %+v", result.MCPServer)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	if provider := lookupMap(cfg, "models", "providers", "aima"); provider != nil {
		t.Fatalf("stale aima provider should be removed when no ready backends remain: %v", provider)
	}
	server := lookupMap(cfg, "mcp", "servers", "aima")
	if server == nil {
		t.Fatal("expected managed mcp server to remain registered")
	}
}

func TestReadConfigSupportsJSON5LikeSyntax(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "openclaw.json")
	data := []byte(`{
  // comments are valid in OpenClaw's config
  "mcp": {
    "servers": {
      "aima": {
        "command": "aima",
        "args": ["mcp",],
      },
    },
  },
}`)
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := ReadConfig(configPath)
	if err != nil {
		t.Fatalf("ReadConfig failed: %v", err)
	}
	server := lookupMap(cfg, "mcp", "servers", "aima")
	if server == nil {
		t.Fatal("mcp.servers.aima missing after JSON5 parse")
	}
	if got := server["command"]; got != "aima" {
		t.Fatalf("command = %v, want aima", got)
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
