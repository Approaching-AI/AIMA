package central

import (
	"context"
	"path/filepath"
	"testing"
)

// mockLLM is a test double for LLMCompleter that returns canned responses.
type mockLLM struct {
	response string
	err      error
}

func (m *mockLLM) Complete(_ context.Context, _, _ string) (string, error) {
	return m.response, m.err
}

func newTestStore(t *testing.T) CentralStore {
	t.Helper()
	store, err := NewSQLiteCentralStore(filepath.Join(t.TempDir(), "advisor_test.db"))
	if err != nil {
		t.Fatalf("NewSQLiteCentralStore: %v", err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestAdvisorRecommend(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{response: `{
		"engine": "vllm",
		"config": {"gpu_memory_utilization": 0.9},
		"quantization": "awq-int4",
		"reasoning": "vLLM is optimal for this GPU",
		"confidence": "high",
		"alternatives": [{"engine": "sglang", "reason": "good alternative"}]
	}`}

	advisor := NewAdvisor(store, llm)

	resp, adv, err := advisor.Recommend(context.Background(), RecommendRequest{
		Hardware: "nvidia-rtx4090",
		Model:    "qwen3-8b",
		Goal:     "low-latency",
	})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if resp.Engine != "vllm" {
		t.Fatalf("Engine = %q, want vllm", resp.Engine)
	}
	if resp.Confidence != "high" {
		t.Fatalf("Confidence = %q, want high", resp.Confidence)
	}
	if adv == nil {
		t.Fatal("advisory should not be nil")
	}
	if adv.Type != AdvisoryTypeConfigRecommend {
		t.Fatalf("advisory type = %q, want %s", adv.Type, AdvisoryTypeConfigRecommend)
	}
	if adv.Status != AdvisoryStatusPending {
		t.Fatalf("status = %q, want pending", adv.Status)
	}

	// Verify advisory was stored
	advs, err := store.ListAdvisories(context.Background(), AdvisoryFilter{Type: AdvisoryTypeConfigRecommend})
	if err != nil {
		t.Fatalf("ListAdvisories: %v", err)
	}
	if len(advs) != 1 {
		t.Fatalf("advisories = %d, want 1", len(advs))
	}
}

func TestAdvisorOptimize(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{response: `{
		"optimizations": [{"type": "parameter", "change": "increase batch size"}],
		"reasoning": "GPU utilization is low",
		"confidence": "medium"
	}`}

	advisor := NewAdvisor(store, llm)

	resp, adv, err := advisor.OptimizeScenario(context.Background(), OptimizeRequest{
		Hardware: "nvidia-rtx4090",
		Model:    "qwen3-8b",
		Engine:   "vllm",
	})
	if err != nil {
		t.Fatalf("OptimizeScenario: %v", err)
	}
	if resp.Confidence != "medium" {
		t.Fatalf("Confidence = %q, want medium", resp.Confidence)
	}
	if len(resp.Optimizations) != 1 {
		t.Fatalf("optimizations = %d, want 1", len(resp.Optimizations))
	}
	if adv.Type != AdvisoryTypeScenarioOptimization {
		t.Fatalf("advisory type = %q, want %s", adv.Type, AdvisoryTypeScenarioOptimization)
	}
}

func TestAdvisorGenerateScenario(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{response: `{
		"name": "dual-model-rtx4090",
		"description": "Two models sharing one GPU",
		"deployments": [{"model": "qwen3-8b", "engine": "vllm"}],
		"total_vram_mib": 24576,
		"reasoning": "50-50 split",
		"confidence": "high"
	}`}

	advisor := NewAdvisor(store, llm)

	resp, scenario, err := advisor.GenerateScenario(context.Background(), GenerateScenarioRequest{
		Hardware: "nvidia-rtx4090",
		Models:   []string{"qwen3-8b", "llama3-8b"},
	})
	if err != nil {
		t.Fatalf("GenerateScenario: %v", err)
	}
	if resp.Name != "dual-model-rtx4090" {
		t.Fatalf("Name = %q, want dual-model-rtx4090", resp.Name)
	}
	if scenario == nil {
		t.Fatal("scenario should not be nil")
	}

	// Verify scenario was stored
	scenarios, err := store.ListScenarios(context.Background(), ScenarioFilter{Hardware: "nvidia-rtx4090"})
	if err != nil {
		t.Fatalf("ListScenarios: %v", err)
	}
	if len(scenarios) != 1 {
		t.Fatalf("scenarios = %d, want 1", len(scenarios))
	}
	if scenarios[0].ScenarioYAML == "" {
		t.Fatal("scenario yaml should not be empty")
	}
}

func TestAdvisorLLMError(t *testing.T) {
	store := newTestStore(t)
	llm := &mockLLM{err: context.DeadlineExceeded}
	advisor := NewAdvisor(store, llm)

	_, _, err := advisor.Recommend(context.Background(), RecommendRequest{
		Hardware: "nvidia-rtx4090",
		Model:    "qwen3-8b",
	})
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
}
