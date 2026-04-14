package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	reflect "reflect"
	"strings"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
)

func newInferenceReadyServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	return server, strings.TrimPrefix(server.URL, "http://")
}

func TestBenchmarkMetadataComplete(t *testing.T) {
	tests := []struct {
		name          string
		concurrency   int
		rounds        int
		totalRequests int
		wantComplete  bool
	}{
		{"all zeros", 0, 0, 0, false},
		{"only concurrency", 4, 0, 0, false},
		{"only rounds", 0, 2, 0, false},
		{"only requests", 0, 0, 10, false},
		{"all valid", 4, 2, 10, true},
		{"minimal valid", 1, 1, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := benchmarkMetadataComplete(tt.concurrency, tt.rounds, tt.totalRequests)
			if got != tt.wantComplete {
				t.Errorf("benchmarkMetadataComplete(%d, %d, %d) = %v, want %v",
					tt.concurrency, tt.rounds, tt.totalRequests, got, tt.wantComplete)
			}
		})
	}
}

func TestExplorationManagerResolveCurrentDeployConfig_UsesReadyDeployment(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			if name != "deploy.status" {
				t.Fatalf("unexpected tool %q", name)
			}
			var args map[string]string
			if err := json.Unmarshal(arguments, &args); err != nil {
				t.Fatalf("Unmarshal deploy.status args: %v", err)
			}
			if args["name"] != "target-model" {
				t.Fatalf("deploy.status name = %q, want target-model", args["name"])
			}
			return &ToolResult{Content: `{"ready":true,"engine":"vllm","config":{"concurrency":4,"max_tokens":512}}`}, nil
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	cfg := manager.resolveCurrentDeployConfig(ctx, "target-model", "vllm")
	want := map[string]any{"concurrency": float64(4), "max_tokens": float64(512)}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("deploy config = %#v, want %#v", cfg, want)
	}
}

func TestExplorationManagerExecuteBenchmarkMatrix_PreservesArtifacts(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var matrixRequest map[string]any
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				return &ToolResult{Content: `{"ready":true,"engine":"vllm","config":{"concurrency":4,"max_tokens":512}}`}, nil
			case "benchmark.matrix":
				if err := json.Unmarshal(arguments, &matrixRequest); err != nil {
					t.Fatalf("Unmarshal benchmark.matrix args: %v", err)
				}
				resp := map[string]any{
					"model": "test-model",
					"cells": []any{
						map[string]any{
							"concurrency":    4,
							"input_tokens":   128,
							"max_tokens":     256,
							"benchmark_id":   "bench-001",
							"config_id":      "cfg-001",
							"engine_version": "1.2.3",
							"engine_image":   "example/engine:1.2.3",
							"resource_usage": map[string]any{"vram_usage_mib": float64(1234)},
							"deploy_config":  map[string]any{"concurrency": float64(4), "max_tokens": float64(512)},
							"result": map[string]any{
								"throughput_tps": 123.4,
								"ttft_p95_ms":    45.6,
							},
						},
					},
					"total": 1,
				}
				data, _ := json.Marshal(resp)
				return &ToolResult{Content: string(data)}, nil
			default:
				t.Fatalf("unexpected tool %q", name)
			}
			return nil, nil
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	result, err := manager.executeBenchmarkMatrix(ctx, &state.ExplorationRun{ID: "run-matrix"}, ExplorationPlan{
		Target: ExplorationTarget{Model: "test-model", Engine: "vllm"},
		BenchmarkProfiles: []ExplorationBenchmarkProfile{{
			Label:             "latency",
			ConcurrencyLevels: []int{4},
			InputTokenLevels:  []int{128},
			MaxTokenLevels:    []int{256},
			RequestsPerCombo:  1,
		}},
	}, "validate", 0)
	if err != nil {
		t.Fatalf("executeBenchmarkMatrix: %v", err)
	}
	if result.TotalCells != 1 || result.SuccessCells != 1 {
		t.Fatalf("matrix counts = (%d,%d), want (1,1)", result.TotalCells, result.SuccessCells)
	}
	if result.BenchmarkID != "bench-001" || result.ConfigID != "cfg-001" {
		t.Fatalf("top-level artifacts = (%q,%q), want (bench-001,cfg-001)", result.BenchmarkID, result.ConfigID)
	}
	if result.EngineVersion != "1.2.3" || result.EngineImage != "example/engine:1.2.3" {
		t.Fatalf("engine metadata = (%q,%q), want propagated metadata", result.EngineVersion, result.EngineImage)
	}
	if !strings.Contains(result.MatrixJSON, "bench-001") || !strings.Contains(result.MatrixJSON, "deploy_config") {
		t.Fatalf("MatrixJSON missing propagated metadata: %s", result.MatrixJSON)
	}
	if !strings.Contains(result.ResponseJSON, `"benchmark_id":"bench-001"`) || !strings.Contains(result.ResponseJSON, `"result":{"throughput_tps":123.4`) {
		t.Fatalf("ResponseJSON missing representative summary: %s", result.ResponseJSON)
	}
	if !reflect.DeepEqual(matrixRequest["deploy_config"], map[string]any{"concurrency": float64(4), "max_tokens": float64(512)}) {
		t.Fatalf("benchmark.matrix deploy_config = %#v, want ready deployment config", matrixRequest["deploy_config"])
	}
}

func TestSummarizeInferenceReadinessFailure_UsesDeployLogs(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			if name != "deploy.logs" {
				t.Fatalf("unexpected tool %q", name)
			}
			return &ToolResult{Content: "INFO booting\nValueError: ModelOptFp8Config only supports static FP8 quantization in SGLang\nINFO retrying"}, nil
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	detail := manager.summarizeInferenceReadinessFailure(ctx, "glm-4-6v-flash-fp4")
	if !strings.Contains(detail, "ModelOptFp8Config") {
		t.Fatalf("detail = %q, want root-cause log line", detail)
	}
}

func TestBuildOpenQuestionActualResultIncludesBenchmarkArtifacts(t *testing.T) {
	got := buildOpenQuestionActualResult(&state.OpenQuestion{
		ID:          "q-1",
		Question:    "Does it work?",
		Expected:    "yes",
		TestCommand: "test",
	}, ExplorationPlan{
		Target: ExplorationTarget{Model: "test-model", Engine: "vllm"},
	}, &benchmarkStepResult{
		BenchmarkID:   "bench-1",
		ConfigID:      "cfg-1",
		EngineVersion: "1.2.3",
		EngineImage:   "example/engine:1.2.3",
		ResourceUsage: map[string]any{"vram_usage_mib": float64(1234)},
		DeployConfig:  map[string]any{"concurrency": float64(4)},
		ResponseJSON:  `{"result":{"throughput_tps":123.4}}`,
	})
	for _, want := range []string{"benchmark_id", "cfg-1", "engine_version", "engine_image", "resource_usage", "deploy_config"} {
		if !strings.Contains(got, want) {
			t.Fatalf("actual result missing %q: %s", want, got)
		}
	}
}

func TestExplorationManagerEnsureDeployed_ContainerRuntimeSkipsConflictScan(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	statusCalls := 0
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				statusCalls++
				var args map[string]string
				if err := json.Unmarshal(arguments, &args); err != nil {
					t.Fatalf("Unmarshal deploy.status args: %v", err)
				}
				if args["name"] != "target-model" {
					t.Fatalf("unexpected deploy.status target %q", args["name"])
				}
				if statusCalls == 1 {
					return nil, fmt.Errorf("not found")
				}
				return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"vllm","runtime":"container","address":%q}`, addr)}, nil
			case "deploy.apply":
				return &ToolResult{Content: `{"name":"target-model","config":{"gpu_memory_utilization":0.8}}`}, nil
			case "deploy.list":
				t.Fatal("deploy.list should not be called for container runtime")
			case "deploy.delete":
				t.Fatal("deploy.delete should never be called automatically")
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	_, err = manager.ensureDeployed(ctx, &state.ExplorationRun{ID: "run-container"}, ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "vllm",
			Runtime: "container",
		},
	})
	if err != nil {
		t.Fatalf("ensureDeployed: %v", err)
	}
}

func TestExplorationManagerEnsureDeployed_NativeRuntimeRefusesToDeleteConflicts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				var args map[string]string
				if err := json.Unmarshal(arguments, &args); err != nil {
					t.Fatalf("Unmarshal deploy.status args: %v", err)
				}
				if args["name"] == "target-model" {
					return nil, fmt.Errorf("not found")
				}
				return &ToolResult{Content: `{"phase":"running","ready":true}`}, nil
			case "deploy.list":
				// Only native deployments should conflict with native runtime.
				return &ToolResult{Content: `[{"name":"foreign-deploy","phase":"running","runtime":"native"}]`}, nil
			case "deploy.delete":
				t.Fatal("deploy.delete should never be called automatically")
			case "deploy.apply":
				t.Fatal("deploy.apply should not run when native slot is busy")
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	_, err = manager.ensureDeployed(ctx, &state.ExplorationRun{ID: "run-native"}, ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "llama.cpp",
			Runtime: "native",
		},
	})
	if err == nil {
		t.Fatal("expected native busy error")
	}
	if !strings.Contains(err.Error(), "explorer will not delete them automatically") {
		t.Fatalf("error = %q, want refusal to auto-delete", err)
	}
	if !strings.Contains(err.Error(), "foreign-deploy") {
		t.Fatalf("error = %q, want conflicting deployment name", err)
	}
}

func TestExplorationManagerEnsureDeployed_DockerDoesNotBlockNative(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	applied := false
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				if applied {
					return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"sglang-kt","runtime":"native","address":%q}`, addr)}, nil
				}
				return nil, fmt.Errorf("not found")
			case "deploy.list":
				// Docker containers should NOT block native runtime.
				return &ToolResult{Content: `[
					{"name":"vllm-model-1","phase":"running","runtime":"docker"},
					{"name":"vllm-model-2","phase":"running","runtime":"docker"}
				]`}, nil
			case "deploy.apply":
				applied = true
				return &ToolResult{Content: `{"name":"target-model"}`}, nil
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	_, err = manager.ensureDeployed(ctx, &state.ExplorationRun{ID: "run-native-ok"}, ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "sglang-kt",
			Runtime: "native",
		},
	})
	if err != nil {
		t.Fatalf("ensureDeployed should succeed when only Docker containers are running: %v", err)
	}
}

func TestExplorationManagerEnsureDeployed_EngineMismatchRedeploys(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	var deleteCalled, applyCalled bool
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				if applyCalled {
					// After deploy.apply, return ready for waitForReady.
					return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"sglang-kt","runtime":"native","address":%q}`, addr)}, nil
				}
				if deleteCalled {
					// After delete, deployment gone — waitForGPURelease sees this.
					return nil, fmt.Errorf("not found")
				}
				// Model deployed on vllm (docker), but we want sglang-kt (native).
				return &ToolResult{Content: `{"phase":"running","ready":true,"engine":"vllm","runtime":"docker"}`}, nil
			case "deploy.delete":
				deleteCalled = true
				return &ToolResult{Content: `{"deleted":true}`}, nil
			case "deploy.apply":
				applyCalled = true
				return &ToolResult{Content: `{"name":"target-model","engine":"sglang-kt"}`}, nil
			case "deploy.list":
				return &ToolResult{Content: `[]`}, nil
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	_, err = manager.ensureDeployed(ctx, &state.ExplorationRun{ID: "run-mismatch"}, ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "sglang-kt",
			Runtime: "native",
		},
	})
	if err != nil {
		t.Fatalf("ensureDeployed: %v", err)
	}
	if !deleteCalled {
		t.Fatal("expected deploy.delete to be called for engine mismatch")
	}
	if !applyCalled {
		t.Fatal("expected deploy.apply to be called after engine mismatch delete")
	}
}

func TestInferModelFamily(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"Qwen2.5-Coder-3B-Instruct", "qwen"},
		{"qwen3-30B-A3B", "qwen"},
		{"GLM-4.6V-Flash", "glm"},
		{"ChatGLM3-6B", "glm"},
		{"CodeGeeX4-All-9B", "glm"},
		{"Llama-3.1-8B-Instruct", "llama"},
		{"Mistral-7B-v0.1", "mistral"},
		{"deepseek-coder-33B", "deepseek"},
		{"MiniCPM-2B", "minicpm"},
		{"Phi-3-mini-4k", "phi"},
		{"gemma-2-9b", "gemma"},
		{"Baichuan2-7B-Chat", "baichuan"},
		{"internlm2-20b", "internlm"},
		{"unknown-model-7B", "unknown"},
		{"", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := inferModelFamily(tt.model)
			if got != tt.want {
				t.Errorf("inferModelFamily(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestInferParameterCount(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"Qwen2.5-Coder-3B-Instruct", "3B"},
		{"GLM-4.1V-9B", "9B"},
		{"Llama-3.1-70B-Instruct", "70B"},
		{"deepseek-v2-236b", "236B"},
		{"Qwen3-0.6B", "0.6B"},
		{"Qwen3-30B-A3B", "30B"},
		{"no-size-model", "unknown"},
		{"", "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := inferParameterCount(tt.model)
			if got != tt.want {
				t.Errorf("inferParameterCount(%q) = %q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestExplorationManagerEnsureDeployed_SameEngineSkipsDeploy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				// Same engine already deployed and ready.
				return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"vllm","runtime":"docker","address":%q}`, addr)}, nil
			case "deploy.apply":
				t.Fatal("deploy.apply should not be called when same engine is already ready")
			case "deploy.delete":
				t.Fatal("deploy.delete should not be called when same engine is already ready")
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	_, err = manager.ensureDeployed(ctx, &state.ExplorationRun{ID: "run-same"}, ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "vllm",
			Runtime: "container",
		},
	})
	if err != nil {
		t.Fatalf("ensureDeployed: %v", err)
	}
}

func TestExplorationManagerExecuteValidate_CleansUpOwnedDeploymentOnSuccess(t *testing.T) {
	ctx := context.Background()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	plan := ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "vllm",
			Runtime: "container",
		},
		BenchmarkProfiles: []ExplorationBenchmarkProfile{{
			Concurrency: 1,
			Rounds:      1,
		}},
	}
	planJSON, _ := json.Marshal(plan)
	run := &state.ExplorationRun{
		ID:       "run-success",
		Kind:     "validate",
		ModelID:  "target-model",
		EngineID: "vllm",
		PlanJSON: string(planJSON),
		Status:   "queued",
	}
	if err := db.InsertExplorationRun(ctx, run); err != nil {
		t.Fatalf("InsertExplorationRun: %v", err)
	}

	var applied, deleted bool
	deleteCalls := 0
	overrideCalls := 0
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				if deleted {
					return nil, fmt.Errorf("not found")
				}
				if applied {
					return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"vllm","runtime":"container","address":%q,"config":{"gpu_memory_utilization":0.8}}`, addr)}, nil
				}
				return nil, fmt.Errorf("not found")
			case "deploy.apply":
				applied = true
				return &ToolResult{Content: `{"name":"target-model","config":{"gpu_memory_utilization":0.8}}`}, nil
			case "benchmark.run":
				return &ToolResult{Content: `{"benchmark_id":"bench-001","config_id":"cfg-001","engine_version":"1.0.0","engine_image":"example/vllm:1.0.0","deploy_config":{"gpu_memory_utilization":0.8},"result":{"throughput_tps":123.4,"qps":12.3,"ttft_p50_ms":40,"ttft_p95_ms":45,"ttft_p99_ms":50,"tpot_p50_ms":5,"tpot_p95_ms":6,"error_rate":0,"total_requests":4,"successful_requests":4,"avg_input_tokens":128,"avg_output_tokens":256,"config":{"concurrency":1,"rounds":1}}}`}, nil
			case "catalog.override":
				overrideCalls++
				return &ToolResult{Content: `{"action":"created"}`}, nil
			case "deploy.delete":
				deleteCalls++
				deleted = true
				return &ToolResult{Content: `{"deleted":true}`}, nil
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	manager.executeValidate(ctx, run)

	if run.Status != "completed" {
		t.Fatalf("run status = %q, want completed (error=%q)", run.Status, run.Error)
	}
	if deleteCalls != 1 {
		t.Fatalf("deploy.delete calls = %d, want 1", deleteCalls)
	}
	if overrideCalls != 1 {
		t.Fatalf("catalog.override calls = %d, want 1", overrideCalls)
	}
}

func TestExplorationManagerExecuteValidate_CleansUpOwnedDeploymentOnBenchmarkFailure(t *testing.T) {
	ctx := context.Background()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	plan := ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "vllm",
			Runtime: "container",
		},
		BenchmarkProfiles: []ExplorationBenchmarkProfile{{
			Concurrency: 1,
			Rounds:      1,
		}},
	}
	planJSON, _ := json.Marshal(plan)
	run := &state.ExplorationRun{
		ID:       "run-failure",
		Kind:     "validate",
		ModelID:  "target-model",
		EngineID: "vllm",
		PlanJSON: string(planJSON),
		Status:   "queued",
	}
	if err := db.InsertExplorationRun(ctx, run); err != nil {
		t.Fatalf("InsertExplorationRun: %v", err)
	}

	var applied, deleted bool
	deleteCalls := 0
	overrideCalls := 0
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				if deleted {
					return nil, fmt.Errorf("not found")
				}
				if applied {
					return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"vllm","runtime":"container","address":%q,"config":{"gpu_memory_utilization":0.8}}`, addr)}, nil
				}
				return nil, fmt.Errorf("not found")
			case "deploy.apply":
				applied = true
				return &ToolResult{Content: `{"name":"target-model","config":{"gpu_memory_utilization":0.8}}`}, nil
			case "benchmark.run":
				return nil, fmt.Errorf("benchmark exploded")
			case "catalog.override":
				overrideCalls++
				return &ToolResult{Content: `{"action":"created"}`}, nil
			case "deploy.delete":
				deleteCalls++
				deleted = true
				return &ToolResult{Content: `{"deleted":true}`}, nil
			}
			return nil, fmt.Errorf("unexpected tool: %s", name)
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	manager.executeValidate(ctx, run)

	if run.Status != "failed" {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "benchmark exploded") {
		t.Fatalf("run error = %q, want benchmark failure", run.Error)
	}
	if deleteCalls != 1 {
		t.Fatalf("deploy.delete calls = %d, want 1", deleteCalls)
	}
	if overrideCalls != 0 {
		t.Fatalf("catalog.override calls = %d, want 0", overrideCalls)
	}
}

func TestExplorationManagerStartAndWait_RespectsCallerDeadline(t *testing.T) {
	ctx := context.Background()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()

	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"vllm","runtime":"container","address":%q,"config":{"gpu_memory_utilization":0.8}}`, addr)}, nil
			case "benchmark.run":
				<-ctx.Done()
				return nil, ctx.Err()
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = manager.StartAndWait(waitCtx, ExplorationStart{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "vllm",
			Runtime: "container",
		},
		BenchmarkProfiles: []ExplorationBenchmarkProfile{{
			Concurrency: 1,
			Rounds:      1,
		}},
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("StartAndWait error = nil, want context deadline exceeded")
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("StartAndWait error = %q, want deadline exceeded", err)
	}
	if strings.Contains(err.Error(), "30 minutes") {
		t.Fatalf("StartAndWait error = %q, should respect caller deadline instead of fallback timeout", err)
	}
	if elapsed > time.Second {
		t.Fatalf("StartAndWait elapsed = %v, want < 1s", elapsed)
	}
}

func TestExplorationManagerExecuteValidate_CancelledContextSkipsLateSuccessArtifacts(t *testing.T) {
	ctx := context.Background()
	server, addr := newInferenceReadyServer(t)
	defer server.Close()

	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	plan := ExplorationPlan{
		Kind: "validate",
		Target: ExplorationTarget{
			Model:   "target-model",
			Engine:  "vllm",
			Runtime: "container",
		},
		BenchmarkProfiles: []ExplorationBenchmarkProfile{{
			Label:             "latency",
			ConcurrencyLevels: []int{1},
			InputTokenLevels:  []int{128},
			MaxTokenLevels:    []int{256},
			RequestsPerCombo:  1,
			Rounds:            1,
		}},
	}
	planJSON, _ := json.Marshal(plan)
	run := &state.ExplorationRun{
		ID:       "run-cancelled-late-success",
		Kind:     "validate",
		ModelID:  "target-model",
		EngineID: "vllm",
		PlanJSON: string(planJSON),
		Status:   "queued",
	}
	if err := db.InsertExplorationRun(ctx, run); err != nil {
		t.Fatalf("InsertExplorationRun: %v", err)
	}

	overrideCalls := 0
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.status":
				return &ToolResult{Content: fmt.Sprintf(`{"phase":"running","ready":true,"engine":"vllm","runtime":"container","address":%q,"config":{"gpu_memory_utilization":0.8}}`, addr)}, nil
			case "benchmark.matrix":
				<-ctx.Done()
				return &ToolResult{Content: `{"cells":[{"concurrency":1,"input_tokens":128,"max_tokens":256,"benchmark_id":"bench-late","config_id":"cfg-late","result":{"throughput_tps":123.4,"ttft_p95_ms":45,"tpot_p95_ms":6}}],"total":1}`}, nil
			case "catalog.override":
				overrideCalls++
				return &ToolResult{Content: `{"action":"created"}`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	runCtx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	manager.executeValidate(runCtx, run)

	if run.Status != "cancelled" {
		t.Fatalf("run status = %q, want cancelled (error=%q)", run.Status, run.Error)
	}
	if overrideCalls != 0 {
		t.Fatalf("catalog.override calls = %d, want 0 after cancellation", overrideCalls)
	}
	stored, err := db.GetExplorationRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetExplorationRun: %v", err)
	}
	if stored.Status != "cancelled" {
		t.Fatalf("stored run status = %q, want cancelled", stored.Status)
	}
}
