package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jguan/aima/internal/engine"
)

func TestDeployRunPassesConfigOverrides(t *testing.T) {
	s := NewServer()

	var (
		gotModel  string
		gotEngine string
		gotSlot   string
		gotConfig map[string]any
		gotNoPull bool
	)
	registerDeployTools(s, &ToolDeps{
		DeployRun: func(ctx context.Context, model, engineType, slot string, configOverrides map[string]any, noPull bool, onPhase func(string, string), onEngineProgress func(engine.ProgressEvent), onModelProgress func(int64, int64)) (json.RawMessage, error) {
			gotModel = model
			gotEngine = engineType
			gotSlot = slot
			gotConfig = configOverrides
			gotNoPull = noPull
			return json.RawMessage(`{"status":"ready"}`), nil
		},
	})

	result, err := s.ExecuteTool(context.Background(), "deploy.run", json.RawMessage(`{
		"model":"qwen3-8b",
		"engine":"vllm",
		"slot":"slot-1",
		"config":{"gpu_memory_utilization":0.9},
		"max_cold_start_s":12,
		"no_pull":true
	}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}

	if gotModel != "qwen3-8b" {
		t.Fatalf("model = %q, want qwen3-8b", gotModel)
	}
	if gotEngine != "vllm" {
		t.Fatalf("engine = %q, want vllm", gotEngine)
	}
	if gotSlot != "slot-1" {
		t.Fatalf("slot = %q, want slot-1", gotSlot)
	}
	if !gotNoPull {
		t.Fatal("expected no_pull=true")
	}
	if gotConfig["gpu_memory_utilization"] != 0.9 {
		t.Fatalf("gpu_memory_utilization = %#v, want 0.9", gotConfig["gpu_memory_utilization"])
	}
	if gotConfig["max_cold_start_s"] != float64(12) && gotConfig["max_cold_start_s"] != 12 {
		t.Fatalf("max_cold_start_s = %#v, want 12", gotConfig["max_cold_start_s"])
	}
	if len(result.Content) == 0 || result.IsError {
		t.Fatalf("unexpected result = %+v", result)
	}
}

func TestDeployApplyPassesNoPull(t *testing.T) {
	s := NewServer()

	var (
		gotModel  string
		gotEngine string
		gotSlot   string
		gotConfig map[string]any
		gotNoPull bool
	)
	registerDeployTools(s, &ToolDeps{
		DeployApply: func(ctx context.Context, engineType, model, slot string, configOverrides map[string]any, noPull bool) (json.RawMessage, error) {
			gotModel = model
			gotEngine = engineType
			gotSlot = slot
			gotConfig = configOverrides
			gotNoPull = noPull
			return json.RawMessage(`{"status":"deploying","name":"demo"}`), nil
		},
	})

	result, err := s.ExecuteTool(context.Background(), "deploy.apply", json.RawMessage(`{
		"model":"qwen3-8b",
		"engine":"vllm",
		"slot":"slot-1",
		"config":{"gpu_memory_utilization":0.9},
		"no_pull":true
	}`))
	if err != nil {
		t.Fatalf("ExecuteTool: %v", err)
	}
	if gotModel != "qwen3-8b" {
		t.Fatalf("model = %q, want qwen3-8b", gotModel)
	}
	if gotEngine != "vllm" {
		t.Fatalf("engine = %q, want vllm", gotEngine)
	}
	if gotSlot != "slot-1" {
		t.Fatalf("slot = %q, want slot-1", gotSlot)
	}
	if !gotNoPull {
		t.Fatal("expected no_pull=true")
	}
	if gotConfig["gpu_memory_utilization"] != 0.9 {
		t.Fatalf("gpu_memory_utilization = %#v, want 0.9", gotConfig["gpu_memory_utilization"])
	}
	if len(result.Content) == 0 || result.IsError {
		t.Fatalf("unexpected result = %+v", result)
	}
}
