package central

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// mockCompleter returns canned LLM responses for testing.
type mockCompleter struct {
	response string
}

func (m *mockCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	return m.response, nil
}

func setupTestStore(t *testing.T) CentralStore {
	t.Helper()
	store, err := NewSQLiteCentralStore(":memory:")
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestAdvisoryLifecycle_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)

	// Step 1: register a device
	err := store.UpsertDevice(ctx, Device{
		ID:      "test-device",
		GPUArch: "Ada",
	})
	if err != nil {
		t.Fatalf("upsert device: %v", err)
	}

	// Step 2: push a configuration
	err = store.InsertConfiguration(ctx, Configuration{
		ID:         "cfg-001",
		DeviceID:   "test-device",
		Hardware:   "nvidia-rtx4090-x86",
		EngineType: "vllm",
		Model:      "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.9}`,
		ConfigHash: "abc123",
		Status:     "experiment",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("insert config: %v", err)
	}

	// Step 3: advisor recommends
	llm := &mockCompleter{response: `{
		"engine": "vllm",
		"config": {"gpu_memory_utilization": 0.85, "tensor_parallel_size": 1},
		"quantization": "awq",
		"reasoning": "Based on existing configs, lowering GMU to 0.85 gives better stability",
		"confidence": "high"
	}`}
	advisor := NewAdvisor(store, llm)
	resp, advisory, err := advisor.Recommend(ctx, RecommendRequest{
		Hardware: "nvidia-rtx4090-x86",
		Model:    "qwen3-8b",
		Goal:     "balanced",
	})
	if err != nil {
		t.Fatalf("recommend: %v", err)
	}
	if resp.Engine != "vllm" {
		t.Errorf("engine = %q, want vllm", resp.Engine)
	}
	if advisory == nil {
		t.Fatal("advisory should not be nil")
	}
	if advisory.ID == "" {
		t.Error("advisory ID should not be empty")
	}

	// Step 4: list advisories -- should include the one we just created
	advs, err := store.ListAdvisories(ctx, AdvisoryFilter{Hardware: "nvidia-rtx4090-x86"})
	if err != nil {
		t.Fatalf("list advisories: %v", err)
	}
	if len(advs) == 0 {
		t.Fatal("expected at least 1 advisory")
	}
	found := false
	for _, a := range advs {
		if a.ID == advisory.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("advisory %s not found in list", advisory.ID)
	}

	// Step 5: send feedback
	err = store.UpdateAdvisoryStatus(ctx, advisory.ID, AdvisoryStatusUpdate{
		Status:      AdvisoryStatusValidated,
		Feedback:    "validated: 42.5 tok/s, stable",
		ValidatedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("update status: %v", err)
	}

	// Step 6: verify feedback was recorded
	advs, _ = store.ListAdvisories(ctx, AdvisoryFilter{})
	for _, a := range advs {
		if a.ID == advisory.ID {
			if !a.Accepted || a.Status != AdvisoryStatusValidated {
				t.Error("advisory should be accepted")
			}
			if a.Feedback == "" {
				t.Error("feedback should not be empty")
			}
			break
		}
	}
}

func TestGapScan_GeneratesAdvisories(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)

	// Seed a device and config without benchmark data
	_ = store.UpsertDevice(ctx, Device{ID: "dev-1", GPUArch: "Grace"})
	_ = store.InsertConfiguration(ctx, Configuration{
		ID:         "cfg-no-bench",
		DeviceID:   "dev-1",
		Hardware:   "nvidia-gb10-arm64",
		EngineType: "vllm",
		Model:      "qwen3-8b",
		Config:     `{"gmu":0.9}`,
		ConfigHash: "hash-no-bench",
		Status:     "experiment",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	})

	// LLM returns gap scan results
	llm := &mockCompleter{response: `[
		{
			"type": "missing_benchmark",
			"hardware": "nvidia-gb10-arm64",
			"model": "qwen3-8b",
			"engine": "vllm",
			"priority": "high",
			"reasoning": "Configuration exists without benchmark data",
			"suggested_action": "Run benchmark with concurrency 1,4,8"
		}
	]`}

	analyzer := NewAnalyzer(store, llm)
	result, err := analyzer.RunGapScan(ctx)
	if err != nil {
		t.Fatalf("gap scan: %v", err)
	}

	if result.AnalysisRun.Status != "completed" {
		t.Errorf("run status = %q, want completed", result.AnalysisRun.Status)
	}
	if len(result.Advisories) == 0 {
		t.Fatal("expected at least 1 advisory from gap scan")
	}

	adv := result.Advisories[0]
	if adv.Type != AdvisoryTypeGapAlert {
		t.Errorf("advisory type = %q, want %s", adv.Type, AdvisoryTypeGapAlert)
	}
	if adv.TargetHardware != "nvidia-gb10-arm64" {
		t.Errorf("advisory hardware = %q, want nvidia-gb10-arm64", adv.TargetHardware)
	}
	if adv.Severity != "warning" {
		t.Errorf("advisory severity = %q, want warning (high priority)", adv.Severity)
	}

	// Verify advisory was persisted
	stored, err := store.ListAdvisories(ctx, AdvisoryFilter{})
	if err != nil {
		t.Fatalf("list advisories: %v", err)
	}
	if len(stored) == 0 {
		t.Error("advisory should be persisted in store")
	}
}

func TestScenarioGeneration_EndToEnd(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)

	// Seed data
	_ = store.UpsertDevice(ctx, Device{ID: "dev-2", GPUArch: "Ada"})
	_ = store.InsertConfiguration(ctx, Configuration{
		ID:         "cfg-scn-1",
		DeviceID:   "dev-2",
		Hardware:   "nvidia-rtx4090-x86",
		EngineType: "vllm",
		Model:      "qwen3-8b",
		Config:     `{"gmu":0.5}`,
		ConfigHash: "hash-scn-1",
		Status:     "golden",
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	})

	llm := &mockCompleter{response: `{
		"name": "dual-model-4090",
		"description": "Run qwen3-8b and glm-4.7-flash on a single 4090",
		"deployments": [
			{"model": "qwen3-8b", "engine": "vllm", "gmu": 0.5},
			{"model": "glm-4.7-flash", "engine": "vllm", "gmu": 0.4}
		],
		"total_vram_mib": 20480,
		"reasoning": "Both models fit with GMU 0.5 + 0.4",
		"confidence": "high"
	}`}
	advisor := NewAdvisor(store, llm)
	resp, scenario, err := advisor.GenerateScenario(ctx, GenerateScenarioRequest{
		Hardware: "nvidia-rtx4090-x86",
		Models:   []string{"qwen3-8b", "glm-4.7-flash"},
		Goal:     "balanced",
	})
	if err != nil {
		t.Fatalf("generate scenario: %v", err)
	}

	if resp.Name != "dual-model-4090" {
		t.Errorf("name = %q, want dual-model-4090", resp.Name)
	}
	if scenario == nil || scenario.ID == "" {
		t.Fatal("scenario should be stored with an ID")
	}
	if scenario.ScenarioYAML == "" {
		t.Fatal("scenario yaml should be populated")
	}

	// Verify scenario was persisted
	scenarios, err := store.ListScenarios(ctx, ScenarioFilter{Hardware: "nvidia-rtx4090-x86"})
	if err != nil {
		t.Fatalf("list scenarios: %v", err)
	}
	if len(scenarios) == 0 {
		t.Error("scenario should be persisted")
	}
}

func TestSyncPullAdvisories_JSONShape(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)

	// Insert an advisory directly
	_ = store.InsertAdvisory(ctx, Advisory{
		ID:             "adv-test-1",
		Type:           AdvisoryTypeConfigRecommend,
		Status:         AdvisoryStatusPending,
		Severity:       "info",
		TargetHardware: "nvidia-gb10-arm64",
		TargetModel:    "qwen3-8b",
		TargetEngine:   "vllm",
		Title:          "Test Advisory",
		Reasoning:      "Test summary",
		ContentJSON:    []byte(`{"engine":"vllm"}`),
		Confidence:     "high",
		CreatedAt:      time.Now().UTC().Format(time.RFC3339),
	})

	// List advisories (mirrors what the edge pulls)
	advs, err := store.ListAdvisories(ctx, AdvisoryFilter{})
	if err != nil {
		t.Fatalf("list advisories: %v", err)
	}
	if len(advs) != 1 {
		t.Fatalf("expected 1 advisory, got %d", len(advs))
	}

	// Verify JSON serialization round-trip (as the edge would receive it)
	data, err := json.Marshal(advs)
	if err != nil {
		t.Fatalf("marshal advisories: %v", err)
	}
	var decoded []Advisory
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal advisories: %v", err)
	}
	if decoded[0].ID != "adv-test-1" {
		t.Errorf("advisory ID = %q, want adv-test-1", decoded[0].ID)
	}
	if decoded[0].TargetModel != "qwen3-8b" {
		t.Errorf("advisory model = %q, want qwen3-8b", decoded[0].TargetModel)
	}
}
