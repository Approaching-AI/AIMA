package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestExplorer_DetectTier(t *testing.T) {
	tests := []struct {
		name     string
		llm      LLMClient
		toolMode string
		wantTier int
	}{
		{"no LLM", nil, "", 0},
		{"context only", &mockLLM{}, "context_only", 1},
		{"tool calling", &mockLLM{}, "enabled", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var a *Agent
			if tt.llm != nil {
				a = NewAgent(tt.llm, &mockTools{})
				a.mode = toolMode(toolModeContextOnly)
				if tt.toolMode == "enabled" {
					a.mode = toolModeEnabled
				}
			}
			e := &Explorer{agent: a}
			tier := e.detectTier()
			if tier != tt.wantTier {
				t.Errorf("detectTier = %d, want %d", tier, tt.wantTier)
			}
		})
	}
}

func TestExplorer_Status(t *testing.T) {
	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
	}, nil, nil, nil, bus)

	status := e.Status()
	if status.Running {
		t.Error("expected not running before Start")
	}
	if status.Tier != 0 {
		t.Errorf("tier = %d, want 0 (no agent)", status.Tier)
	}
	if status.Enabled {
		t.Error("expected explorer enabled flag to default to false")
	}
}

func TestExplorer_UpdateConfig(t *testing.T) {
	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, nil, nil, nil, bus)

	if _, err := e.UpdateConfig("gap_scan_interval", "30m"); err != nil {
		t.Fatalf("UpdateConfig gap_scan_interval: %v", err)
	}
	if _, err := e.UpdateConfig("enabled", "false"); err != nil {
		t.Fatalf("UpdateConfig enabled: %v", err)
	}

	status := e.Status()
	if status.Schedule.GapScanInterval != 30*time.Minute {
		t.Fatalf("gap scan interval = %v, want 30m", status.Schedule.GapScanInterval)
	}
	if status.Enabled {
		t.Fatal("expected explorer to be disabled after update")
	}
}

func TestExplorer_BudgetModeLimitsRounds(t *testing.T) {
	bus := NewEventBus()
	plansExecuted := 0
	// Create a minimal agent so detectTier() returns 1 (context_only)
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	e := NewExplorer(ExplorerConfig{
		Schedule:  DefaultScheduleConfig(),
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 2,
	}, agent, nil, nil, bus,
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "test"}, nil
		}),
	)
	// Override planner to count executions
	e.planner = &countingPlanner{executed: &plansExecuted}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go e.Start(ctx)
	time.Sleep(10 * time.Millisecond)

	// Fire 5 events — only 2 should produce plan execution
	for i := 0; i < 5; i++ {
		bus.Publish(ExplorerEvent{Type: EventScheduledGapScan})
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)

	if plansExecuted != 2 {
		t.Errorf("plansExecuted = %d, want 2 (maxRounds)", plansExecuted)
	}
}

// countingPlanner is a test planner that generates 1-task plans and counts invocations.
type countingPlanner struct {
	executed *int
}

func (p *countingPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	*p.executed++
	return &ExplorerPlan{
		ID:    fmt.Sprintf("test-%d", *p.executed),
		Tier:  1,
		Tasks: []PlanTask{{Kind: "validate", Model: "m", Engine: "e", Priority: 0}},
	}, 0, nil
}

func TestParseAdvisoryTaskCarriesConfigAndHardware(t *testing.T) {
	taskInfo, task, err := parseAdvisoryTask(json.RawMessage(`{
		"id":"adv-1",
		"type":"recommendation",
		"target_model":"qwen3-8b",
		"target_engine":"vllm",
		"content_json":{"gpu_memory_utilization":0.8}
	}`), "nvidia-gb10-arm64")
	if err != nil {
		t.Fatalf("parseAdvisoryTask: %v", err)
	}
	if taskInfo.ID != "adv-1" {
		t.Fatalf("id = %q, want adv-1", taskInfo.ID)
	}
	if task.Hardware != "nvidia-gb10-arm64" {
		t.Fatalf("hardware = %q, want nvidia-gb10-arm64", task.Hardware)
	}
	if task.Params["gpu_memory_utilization"] != 0.8 {
		t.Fatalf("params = %v, want gpu_memory_utilization", task.Params)
	}
	if task.SourceRef != "adv-1" {
		t.Fatalf("source_ref = %q, want adv-1", task.SourceRef)
	}
}
