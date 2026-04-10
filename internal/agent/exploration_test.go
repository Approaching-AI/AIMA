package agent

import (
	"context"
	"encoding/json"
	"fmt"
	reflect "reflect"
	"strings"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
)

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
	if !strings.Contains(result.MatrixJSON, "bench-001") || !strings.Contains(result.MatrixJSON, "deploy_config") {
		t.Fatalf("MatrixJSON missing propagated metadata: %s", result.MatrixJSON)
	}
	if !reflect.DeepEqual(matrixRequest["deploy_config"], map[string]any{"concurrency": float64(4), "max_tokens": float64(512)}) {
		t.Fatalf("benchmark.matrix deploy_config = %#v, want ready deployment config", matrixRequest["deploy_config"])
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
				return &ToolResult{Content: `{"phase":"running","ready":true}`}, nil
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
					return &ToolResult{Content: `{"phase":"running","ready":true,"engine":"sglang-kt","runtime":"native"}`}, nil
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
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
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
					return &ToolResult{Content: `{"phase":"running","ready":true,"engine":"sglang-kt","runtime":"native"}`}, nil
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

func TestExplorationManagerEnsureDeployed_SameEngineSkipsDeploy(t *testing.T) {
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
				// Same engine already deployed and ready.
				return &ToolResult{Content: `{"phase":"running","ready":true,"engine":"vllm","runtime":"docker"}`}, nil
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
