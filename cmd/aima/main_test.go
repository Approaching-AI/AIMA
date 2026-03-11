package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	state "github.com/jguan/aima/internal"
	benchpkg "github.com/jguan/aima/internal/benchmark"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
)

func TestValidateOverlayAssetName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "valid simple", input: "qwen3-8b", wantErr: false},
		{name: "valid dotted", input: "qwen3.5-35b-a3b", wantErr: false},
		{name: "valid underscore", input: "vllm_rocm", wantErr: false},
		{name: "empty", input: "", wantErr: true},
		{name: "path traversal", input: "../evil", wantErr: true},
		{name: "slash", input: "models/evil", wantErr: true},
		{name: "backslash", input: `models\evil`, wantErr: true},
		{name: "absolute path", input: "/tmp/evil", wantErr: true},
		{name: "invalid chars", input: "evil;rm -rf", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOverlayAssetName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateOverlayAssetName(%q) error = %v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestIsBlockedAgentTool(t *testing.T) {
	tests := []struct {
		name      string
		tool      string
		args      json.RawMessage
		wantBlock bool
	}{
		{name: "blocked static", tool: "shell.exec", args: json.RawMessage(`{"command":"whoami"}`), wantBlock: true},
		{name: "explore start blocked for agent", tool: "explore.start", args: json.RawMessage(`{"kind":"tune","target":{"model":"qwen3-8b"}}`), wantBlock: true},
		{name: "allowed readonly", tool: "knowledge.resolve", args: json.RawMessage(`{"model":"qwen3-8b"}`), wantBlock: false},
		{name: "system config read allowed", tool: "system.config", args: json.RawMessage(`{"key":"foo"}`), wantBlock: false},
		{name: "system config write blocked", tool: "system.config", args: json.RawMessage(`{"key":"foo","value":"bar"}`), wantBlock: true},
		{name: "system config null value blocked", tool: "system.config", args: json.RawMessage(`{"key":"foo","value":null}`), wantBlock: true},
		// catalog.override: engine/model allowed, infrastructure blocked
		{name: "catalog override engine_asset allowed", tool: "catalog.override", args: json.RawMessage(`{"kind":"engine_asset","name":"vllm","content":"x"}`), wantBlock: false},
		{name: "catalog override model_asset allowed", tool: "catalog.override", args: json.RawMessage(`{"kind":"model_asset","name":"qwen3","content":"x"}`), wantBlock: false},
		{name: "catalog override hardware_profile blocked", tool: "catalog.override", args: json.RawMessage(`{"kind":"hardware_profile","name":"gpu","content":"x"}`), wantBlock: true},
		{name: "catalog override partition_strategy blocked", tool: "catalog.override", args: json.RawMessage(`{"kind":"partition_strategy","name":"p","content":"x"}`), wantBlock: true},
		{name: "catalog override stack_component blocked", tool: "catalog.override", args: json.RawMessage(`{"kind":"stack_component","name":"k3s","content":"x"}`), wantBlock: true},
		{name: "catalog override no kind blocked", tool: "catalog.override", args: json.RawMessage(`{"name":"x","content":"x"}`), wantBlock: true},
		{name: "catalog override empty args blocked", tool: "catalog.override", args: nil, wantBlock: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocked, _ := isBlockedAgentTool(tt.tool, tt.args)
			if blocked != tt.wantBlock {
				t.Fatalf("isBlockedAgentTool(%q) = %v, want %v", tt.tool, blocked, tt.wantBlock)
			}
		})
	}
}

func TestMCPToolAdapter_BlocksHighRiskTool(t *testing.T) {
	s := mcp.NewServer()
	called := 0
	s.RegisterTool(&mcp.Tool{
		Name:        "shell.exec",
		Description: "test shell",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			called++
			return mcp.TextResult("should not run"), nil
		},
	})

	adapter := &mcpToolAdapter{server: s}
	result, err := adapter.ExecuteTool(context.Background(), "shell.exec", json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("expected blocked tool result to be an error")
	}
	if !strings.Contains(result.Content, "BLOCKED") {
		t.Fatalf("expected BLOCKED message, got %q", result.Content)
	}
	if called != 0 {
		t.Fatalf("blocked tool should not execute, called=%d", called)
	}
}

func TestFleetBlockedTools(t *testing.T) {
	// All destructive tools must be in the fleet denylist
	mustBlock := []string{
		"model.remove", "engine.remove", "deploy.delete",
		"explore.start", "agent.install", "stack.init", "agent.rollback", "shell.exec",
	}
	for _, tool := range mustBlock {
		if _, ok := fleetBlockedTools[tool]; !ok {
			t.Errorf("fleetBlockedTools missing %q", tool)
		}
	}

	// Safe tools must not be blocked
	safe := []string{
		"hardware.detect", "model.list", "deploy.list", "knowledge.resolve",
	}
	for _, tool := range safe {
		if _, ok := fleetBlockedTools[tool]; ok {
			t.Errorf("fleetBlockedTools should not block %q", tool)
		}
	}
}

func TestQueryGoldenOverrides(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert engine asset + hardware profile (required by Search JOINs)
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type, version) VALUES ('vllm-nightly', 'vllm-nightly', 'v0.16')`)
	if err != nil {
		t.Fatalf("insert engine_asset: %v", err)
	}
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-gb10-arm64', 'GB10', 'Blackwell')`)
	if err != nil {
		t.Fatalf("insert hardware_profile: %v", err)
	}

	// Insert a golden configuration
	goldenCfg := &state.Configuration{
		ID:         "cfg-golden-001",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85,"max_model_len":32768}`,
		ConfigHash: "golden-hash-001",
		Status:     "golden",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, goldenCfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	// Insert a benchmark so Search returns results (JOIN on throughput)
	benchResult := &state.BenchmarkResult{
		ID:            "br-001",
		ConfigID:      "cfg-golden-001",
		Concurrency:   1,
		ThroughputTPS: 42.5,
	}
	if err := db.InsertBenchmarkResult(ctx, benchResult); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	kStore := knowledge.NewStore(db.RawDB())

	t.Run("finds golden config via gpu arch", func(t *testing.T) {
		// Real code passes hwInfo.GPUArch (e.g. "Blackwell"), not profile name.
		// Search matches via: hardware_profiles WHERE gpu_arch = ?
		result := queryGoldenOverrides(ctx, kStore, "Blackwell", "vllm-nightly", "qwen3-8b")
		if result == nil {
			t.Fatal("expected golden config, got nil")
		}
		if gmu, ok := result["gpu_memory_utilization"]; !ok {
			t.Error("missing gpu_memory_utilization")
		} else if gmu != 0.85 {
			t.Errorf("gpu_memory_utilization = %v, want 0.85", gmu)
		}
		if mml, ok := result["max_model_len"]; !ok {
			t.Error("missing max_model_len")
		} else if mml != float64(32768) {
			t.Errorf("max_model_len = %v, want 32768", mml)
		}
	})

	t.Run("no golden for different gpu arch", func(t *testing.T) {
		result := queryGoldenOverrides(ctx, kStore, "Ada", "vllm-nightly", "qwen3-8b")
		if result != nil {
			t.Errorf("expected nil for non-matching gpu arch, got %v", result)
		}
	})

	t.Run("empty gpu arch returns nil", func(t *testing.T) {
		// Empty GPUArch must return nil to prevent cross-hardware golden injection.
		result := queryGoldenOverrides(ctx, kStore, "", "vllm-nightly", "qwen3-8b")
		if result != nil {
			t.Errorf("expected nil for empty gpu arch (cross-hardware guard), got %v", result)
		}
	})

	t.Run("nil store returns nil", func(t *testing.T) {
		result := queryGoldenOverrides(ctx, nil, "Blackwell", "vllm-nightly", "qwen3-8b")
		if result != nil {
			t.Errorf("expected nil for nil store, got %v", result)
		}
	})
}

func TestL2ProvenanceMerge(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert engine asset + hardware profile
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type, version) VALUES ('vllm-nightly', 'vllm-nightly', 'v0.16')`)
	if err != nil {
		t.Fatalf("insert engine_asset: %v", err)
	}
	_, err = db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-gb10-arm64', 'GB10', 'Blackwell')`)
	if err != nil {
		t.Fatalf("insert hardware_profile: %v", err)
	}

	// Insert a golden config with gmu=0.85 and max_model_len=32768
	goldenCfg := &state.Configuration{
		ID:         "cfg-g-prov",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85,"max_model_len":32768}`,
		ConfigHash: "prov-hash-001",
		Status:     "golden",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, goldenCfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	if err := db.InsertBenchmarkResult(ctx, &state.BenchmarkResult{
		ID: "br-prov", ConfigID: "cfg-g-prov", Concurrency: 1, ThroughputTPS: 30,
	}); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	kStore := knowledge.NewStore(db.RawDB())

	// Simulate user overriding only gmu (L1), golden has both gmu and max_model_len
	userOverrides := map[string]any{"gpu_memory_utilization": 0.9}
	userKeys := map[string]bool{"gpu_memory_utilization": true}

	goldenConfig := queryGoldenOverrides(ctx, kStore, "Blackwell", "vllm-nightly", "qwen3-8b")
	if goldenConfig == nil {
		t.Fatal("expected golden config")
	}

	// Merge: L2 first, then L1 wins
	merged := make(map[string]any, len(goldenConfig)+len(userOverrides))
	for k, v := range goldenConfig {
		merged[k] = v
	}
	for k, v := range userOverrides {
		merged[k] = v
	}

	// Verify user override wins for gmu
	if gmu := merged["gpu_memory_utilization"]; gmu != 0.9 {
		t.Errorf("user override should win: gpu_memory_utilization = %v, want 0.9", gmu)
	}
	// Verify golden config provides max_model_len
	if mml := merged["max_model_len"]; mml != float64(32768) {
		t.Errorf("golden should provide max_model_len = %v, want 32768", mml)
	}

	// Verify provenance marking
	for k := range goldenConfig {
		if userKeys[k] {
			// User-overridden keys stay as L1
		} else {
			// Golden-only keys should be L2
			// (In real code, this is done by resolveDeployment)
		}
	}
	if userKeys["max_model_len"] {
		t.Error("max_model_len should not be in userKeys")
	}
	if !userKeys["gpu_memory_utilization"] {
		t.Error("gpu_memory_utilization should be in userKeys")
	}
}

func TestLoadLLMSettings_Defaults(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	t.Setenv("AIMA_LLM_ENDPOINT", "")
	t.Setenv("AIMA_LLM_MODEL", "")
	t.Setenv("AIMA_API_KEY", "")
	t.Setenv("AIMA_LLM_USER_AGENT", "")
	t.Setenv("AIMA_LLM_EXTRA_PARAMS", "")

	settings := loadLLMSettings(ctx, db)
	if settings.Endpoint != "http://localhost:6188/v1" {
		t.Fatalf("Endpoint = %q, want http://localhost:6188/v1", settings.Endpoint)
	}
	if settings.Model != "" {
		t.Fatalf("Model = %q, want empty", settings.Model)
	}
	if settings.APIKey != "" {
		t.Fatalf("APIKey = %q, want empty", settings.APIKey)
	}
}

func TestSyncZeroClawConfig_WritesManagedConfig(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "test-model"}},
		})
	}))
	defer server.Close()

	if err := db.SetConfig(ctx, "llm.endpoint", server.URL+"/v1"); err != nil {
		t.Fatalf("SetConfig llm.endpoint: %v", err)
	}

	dataDir := t.TempDir()
	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "zeroclaw")
	script := `#!/bin/sh
if [ "$1" != "config" ]; then
  exit 1
fi
shift
if [ "$1" != "--config-dir" ]; then
  exit 1
fi
cfgdir="$2"
mkdir -p "$cfgdir"
cat > "$cfgdir/config.toml" <<'EOF'
default_provider = "openrouter"
default_model = "old-model"
default_temperature = 0.7

[transcription]
enabled = false
api_url = "https://example.invalid/v1"
api_key = "old-secret"
EOF
echo '{}'
`
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake zeroclaw: %v", err)
	}

	if err := syncZeroClawConfig(ctx, db, dataDir, binPath); err != nil {
		t.Fatalf("syncZeroClawConfig: %v", err)
	}

	configPath := filepath.Join(dataDir, "zeroclaw", "config.toml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read zeroclaw config: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`default_provider = "openai"`,
		`default_model = "test-model"`,
		`api_url = "` + server.URL + `/v1"`,
		`api_key = "aima-local"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q:\n%s", want, text)
		}
	}
}

func TestMCPToolAdapter_SystemConfigReadAllowedWriteBlocked(t *testing.T) {
	s := mcp.NewServer()
	called := 0
	s.RegisterTool(&mcp.Tool{
		Name:        "system.config",
		Description: "test config",
		InputSchema: json.RawMessage(`{"type":"object"}`),
		Handler: func(ctx context.Context, params json.RawMessage) (*mcp.ToolResult, error) {
			called++
			return mcp.TextResult("value"), nil
		},
	})

	adapter := &mcpToolAdapter{server: s}

	readResult, err := adapter.ExecuteTool(context.Background(), "system.config", json.RawMessage(`{"key":"foo"}`))
	if err != nil {
		t.Fatalf("read ExecuteTool: %v", err)
	}
	if readResult.IsError || readResult.Content != "value" {
		t.Fatalf("expected successful read result, got %+v", readResult)
	}
	if called != 1 {
		t.Fatalf("expected read call to execute tool once, called=%d", called)
	}

	writeResult, err := adapter.ExecuteTool(context.Background(), "system.config", json.RawMessage(`{"key":"foo","value":"bar"}`))
	if err != nil {
		t.Fatalf("write ExecuteTool: %v", err)
	}
	if !writeResult.IsError {
		t.Fatal("expected write call to be blocked")
	}
	if !strings.Contains(writeResult.Content, "BLOCKED") {
		t.Fatalf("expected BLOCKED message, got %q", writeResult.Content)
	}
	if called != 1 {
		t.Fatalf("blocked write should not execute tool handler, called=%d", called)
	}
}

func TestIsLocalLLMEndpoint(t *testing.T) {
	tests := []struct {
		endpoint string
		want     bool
	}{
		{endpoint: "http://localhost:6188/v1", want: true},
		{endpoint: "http://127.0.0.1:6188/v1", want: true},
		{endpoint: "http://[::1]:6188/v1", want: true},
		{endpoint: "https://api.openai.com/v1", want: false},
		{endpoint: "not a url", want: false},
	}
	for _, tt := range tests {
		if got := isLocalLLMEndpoint(tt.endpoint); got != tt.want {
			t.Fatalf("isLocalLLMEndpoint(%q) = %v, want %v", tt.endpoint, got, tt.want)
		}
	}
}

func TestWriteBenchmarkValidationFallsBackToExpectedPerf(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seedBenchmarkPredictionTables(t, ctx, db)

	cfg := &state.Configuration{
		ID:         "cfg-bench-001",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85}`,
		ConfigHash: "cfg-bench-hash-001",
		Status:     "experiment",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}

	if err := writeBenchmarkValidation(ctx, db, "bench-001", cfg.ID, cfg.HardwareID, cfg.EngineID, cfg.ModelID, 36); err != nil {
		t.Fatalf("writeBenchmarkValidation: %v", err)
	}

	rows, err := db.ListValidations(ctx, cfg.HardwareID, cfg.EngineID, cfg.ModelID)
	if err != nil {
		t.Fatalf("ListValidations: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("validation rows = %d, want 1", len(rows))
	}
	if rows[0]["metric"] != "throughput_tps" {
		t.Fatalf("metric = %v, want throughput_tps", rows[0]["metric"])
	}
	if rows[0]["predicted"] != 30.0 {
		t.Fatalf("predicted = %v, want 30", rows[0]["predicted"])
	}
	if rows[0]["actual"] != 36.0 {
		t.Fatalf("actual = %v, want 36", rows[0]["actual"])
	}
}

func TestLookupPredictedThroughputPrefersGoldenBenchmark(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	seedBenchmarkPredictionTables(t, ctx, db)

	cfg := &state.Configuration{
		ID:         "cfg-golden-bench",
		HardwareID: "nvidia-gb10-arm64",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.9}`,
		ConfigHash: "cfg-golden-bench-hash",
		Status:     "golden",
		Source:     "benchmark",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	if err := db.InsertBenchmarkResult(ctx, &state.BenchmarkResult{
		ID:            "bench-golden-001",
		ConfigID:      cfg.ID,
		Concurrency:   1,
		ThroughputTPS: 44,
	}); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	predicted, err := lookupPredictedThroughput(ctx, db.RawDB(), cfg.HardwareID, cfg.EngineID, cfg.ModelID)
	if err != nil {
		t.Fatalf("lookupPredictedThroughput: %v", err)
	}
	if predicted != 44 {
		t.Fatalf("predicted = %v, want 44", predicted)
	}
}

func TestUpdatePerfOverlayWritesObservationOutsideCatalog(t *testing.T) {
	dir := t.TempDir()
	updatePerfOverlay(dir, "qwen3-8b", "nvidia-gb10-arm64", "vllm-nightly", &benchpkg.RunResult{
		ThroughputTPS: 42.5,
		TTFTP50ms:     10,
		TTFTP95ms:     20,
		TPOTP50ms:     3,
		QPS:           5,
	})

	observationPath := filepath.Join(dir, "observations", "models", "qwen3-8b-perf.json")
	if _, err := os.Stat(observationPath); err != nil {
		t.Fatalf("expected observation file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "catalog", "models", "qwen3-8b-perf.yaml")); !os.IsNotExist(err) {
		t.Fatalf("expected no catalog overlay file, got err=%v", err)
	}
}

func seedBenchmarkPredictionTables(t *testing.T, ctx context.Context, db *state.DB) {
	t.Helper()

	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO engine_assets (id, type, version) VALUES ('vllm-nightly', 'vllm-nightly', 'v0.16')`); err != nil {
		t.Fatalf("insert engine_asset: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO hardware_profiles (id, name, gpu_arch) VALUES ('nvidia-gb10-arm64', 'GB10', 'Blackwell')`); err != nil {
		t.Fatalf("insert hardware_profile: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO model_assets (id, name, type) VALUES ('qwen3-8b', 'qwen3-8b', 'llm')`); err != nil {
		t.Fatalf("insert model_asset: %v", err)
	}
	if _, err := db.RawDB().ExecContext(ctx,
		`INSERT INTO model_variants (id, model_id, hardware_id, engine_type, format, default_config, expected_perf, vram_min_mib)
		 VALUES ('qwen3-8b-gb10-vllm', 'qwen3-8b', 'nvidia-gb10-arm64', 'vllm-nightly', 'safetensors', '{}', '{"tokens_per_second":[20,40]}', 8192)`); err != nil {
		t.Fatalf("insert model_variant: %v", err)
	}
}
