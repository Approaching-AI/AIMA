package mcp

import (
	"context"
	"encoding/json"
	"strings"
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

func TestDeployDryRunPassesConfigOverrides(t *testing.T) {
	s := NewServer()

	var (
		gotModel  string
		gotEngine string
		gotSlot   string
		gotConfig map[string]any
	)
	registerDeployTools(s, &ToolDeps{
		DeployDryRun: func(ctx context.Context, engineType, model, slot string, configOverrides map[string]any) (json.RawMessage, error) {
			gotModel = model
			gotEngine = engineType
			gotSlot = slot
			gotConfig = configOverrides
			return json.RawMessage(`{"status":"preview"}`), nil
		},
	})

	result, err := s.ExecuteTool(context.Background(), "deploy.dry_run", json.RawMessage(`{
		"model":"qwen3-8b",
		"engine":"vllm",
		"slot":"slot-1",
		"config":{"gpu_memory_utilization":0.8,"kv_cache_dtype":"fp8"},
		"max_cold_start_s":12
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
	if gotConfig["gpu_memory_utilization"] != 0.8 {
		t.Fatalf("gpu_memory_utilization = %#v, want 0.8", gotConfig["gpu_memory_utilization"])
	}
	if gotConfig["kv_cache_dtype"] != "fp8" {
		t.Fatalf("kv_cache_dtype = %#v, want fp8", gotConfig["kv_cache_dtype"])
	}
	if gotConfig["max_cold_start_s"] != float64(12) && gotConfig["max_cold_start_s"] != 12 {
		t.Fatalf("max_cold_start_s = %#v, want 12", gotConfig["max_cold_start_s"])
	}
	if len(result.Content) == 0 || result.IsError {
		t.Fatalf("unexpected result = %+v", result)
	}
}

func TestDeployDefaultsStoresDeviceLocalSettings(t *testing.T) {
	s := NewServer()
	store := map[string]string{}
	registerDeployTools(s, &ToolDeps{
		GetConfig: func(ctx context.Context, key string) (string, error) {
			value, ok := store[key]
			if !ok {
				return "", context.Canceled
			}
			return value, nil
		},
		SetConfig: func(ctx context.Context, key, value string) error {
			store[key] = value
			return nil
		},
	})

	setResult, err := s.ExecuteTool(context.Background(), "deploy.defaults", json.RawMessage(`{
		"action":"set",
		"model":"Qwen3-8B",
		"engine":"vllm",
		"slot":"slot-1",
		"no_pull":true,
		"port":"8003",
		"config":{"gpu_memory_utilization":0.7,"max_model_len":8192}
	}`))
	if err != nil {
		t.Fatalf("set ExecuteTool: %v", err)
	}
	if setResult.IsError {
		t.Fatalf("set returned error: %+v", setResult)
	}

	getResult, err := s.ExecuteTool(context.Background(), "deploy.defaults", json.RawMessage(`{"action":"get","model":"qwen3-8b"}`))
	if err != nil {
		t.Fatalf("get ExecuteTool: %v", err)
	}
	if getResult.IsError {
		t.Fatalf("get returned error: %+v", getResult)
	}
	var got struct {
		Exists   bool `json:"exists"`
		Defaults struct {
			Engine string         `json:"engine"`
			Slot   string         `json:"slot"`
			NoPull bool           `json:"no_pull"`
			Port   string         `json:"port"`
			Config map[string]any `json:"config"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal([]byte(getResult.Content[0].Text), &got); err != nil {
		t.Fatalf("unmarshal get result: %v", err)
	}
	if !got.Exists {
		t.Fatal("defaults should exist")
	}
	if got.Defaults.Engine != "vllm" || got.Defaults.Slot != "slot-1" || !got.Defaults.NoPull || got.Defaults.Port != "8003" {
		t.Fatalf("defaults = %+v", got.Defaults)
	}
	if got.Defaults.Config["gpu_memory_utilization"] != float64(0.7) {
		t.Fatalf("gpu_memory_utilization = %#v, want 0.7", got.Defaults.Config["gpu_memory_utilization"])
	}

	clearResult, err := s.ExecuteTool(context.Background(), "deploy.defaults", json.RawMessage(`{"action":"clear","model":"qwen3-8b"}`))
	if err != nil {
		t.Fatalf("clear ExecuteTool: %v", err)
	}
	if clearResult.IsError {
		t.Fatalf("clear returned error: %+v", clearResult)
	}
	getResult, err = s.ExecuteTool(context.Background(), "deploy.defaults", json.RawMessage(`{"action":"get","model":"qwen3-8b"}`))
	if err != nil {
		t.Fatalf("get after clear ExecuteTool: %v", err)
	}
	if !strings.Contains(getResult.Content[0].Text, `"exists":false`) {
		t.Fatalf("get after clear = %s, want exists=false", getResult.Content[0].Text)
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
