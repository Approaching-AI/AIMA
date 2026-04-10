package agent

import (
	"context"
	"testing"
	"time"
)

func TestExplorer_EventTriggersPlanning(t *testing.T) {
	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, nil, nil, nil, bus,
		WithGatherGaps(func(ctx context.Context) ([]GapEntry, error) {
			return []GapEntry{
				{Model: "qwen3-8b", Engine: "vllm"},
			}, nil
		}),
		WithGatherDeploys(func(ctx context.Context) ([]DeployStatus, error) {
			return []DeployStatus{
				{Model: "qwen3-8b", Engine: "vllm", Status: "running"},
			}, nil
		}),
	)

	// Tier 0 (no agent) means events are skipped -- verify Status works
	status := e.Status()
	if status.Tier != 0 {
		t.Errorf("tier = %d, want 0 (no agent)", status.Tier)
	}
	if status.Running {
		t.Error("expected not running before Start")
	}
}

func TestExplorer_WithAgentDetectsTier(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "ok"}},
	}
	a := NewAgent(llm, &mockTools{})
	a.mode = toolModeContextOnly

	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, a, nil, nil, bus)

	if e.tier != 1 {
		t.Errorf("tier = %d, want 1 (context_only)", e.tier)
	}

	status := e.Status()
	if status.Tier != 1 {
		t.Errorf("status tier = %d, want 1", status.Tier)
	}
}

func TestExplorer_BuildPlanInputGathersData(t *testing.T) {
	bus := NewEventBus()
	hardwareCalled := false
	gapsCalled := false
	deploysCalled := false
	openQuestionsCalled := false

	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
	}, nil, nil, nil, bus,
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			hardwareCalled = true
			return HardwareInfo{Profile: "nvidia-gb10-arm64", GPUArch: "blackwell"}, nil
		}),
		WithGatherGaps(func(ctx context.Context) ([]GapEntry, error) {
			gapsCalled = true
			return []GapEntry{{Model: "test-model", Engine: "vllm"}}, nil
		}),
		WithGatherDeploys(func(ctx context.Context) ([]DeployStatus, error) {
			deploysCalled = true
			return []DeployStatus{{Model: "test-model", Engine: "vllm", Status: "running"}}, nil
		}),
		WithGatherOpenQuestions(func(ctx context.Context) ([]OpenQuestion, error) {
			openQuestionsCalled = true
			return []OpenQuestion{{ID: "oq-1", Model: "test-model", Status: "untested"}}, nil
		}),
	)

	ev := &ExplorerEvent{Type: EventScheduledGapScan}
	input, err := e.buildPlanInput(context.Background(), ev)
	if err != nil {
		t.Fatalf("buildPlanInput: %v", err)
	}

	if !hardwareCalled {
		t.Error("gatherHardware not called")
	}
	if !gapsCalled {
		t.Error("gatherGaps not called")
	}
	if !deploysCalled {
		t.Error("gatherDeploys not called")
	}
	if !openQuestionsCalled {
		t.Error("gatherOpenQuestions not called")
	}
	if input.Hardware.Profile != "nvidia-gb10-arm64" {
		t.Errorf("hardware profile = %q, want nvidia-gb10-arm64", input.Hardware.Profile)
	}
	if len(input.Gaps) != 1 {
		t.Errorf("gaps = %d, want 1", len(input.Gaps))
	}
	if len(input.ActiveDeploys) != 1 {
		t.Errorf("deploys = %d, want 1", len(input.ActiveDeploys))
	}
	if len(input.OpenQuestions) != 1 {
		t.Errorf("open questions = %d, want 1", len(input.OpenQuestions))
	}
	if input.Event.Type != EventScheduledGapScan {
		t.Errorf("event type = %q, want %q", input.Event.Type, EventScheduledGapScan)
	}
}

func TestExplorer_StartAndStop(t *testing.T) {
	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, nil, nil, nil, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	go e.Start(ctx)

	// Give it a moment to start
	time.Sleep(20 * time.Millisecond)

	status := e.Status()
	if !status.Running {
		t.Error("expected running after Start")
	}

	e.Stop()
	time.Sleep(20 * time.Millisecond)

	status = e.Status()
	if status.Running {
		t.Error("expected not running after Stop")
	}
}
