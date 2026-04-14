package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	reflect "reflect"
	"strings"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
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
	if _, err := e.UpdateConfig("max_cycles", "4"); err != nil {
		t.Fatalf("UpdateConfig max_cycles: %v", err)
	}
	if _, err := e.UpdateConfig("max_tasks", "7"); err != nil {
		t.Fatalf("UpdateConfig max_tasks: %v", err)
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
	if status.MaxCycles != 4 {
		t.Fatalf("max cycles = %d, want 4", status.MaxCycles)
	}
	if status.MaxTasks != 7 {
		t.Fatalf("max tasks = %d, want 7", status.MaxTasks)
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

// emptyPlanner always returns 0-task plans (simulates all tasks deduped post-hoc).
type emptyPlanner struct {
	calls *int
}

func (p *emptyPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	*p.calls++
	return &ExplorerPlan{ID: fmt.Sprintf("empty-%d", *p.calls), Tier: 2, Tasks: nil}, 100, nil
}

type refreshTrackingPlanner struct {
	planCalls          int
	analyzeCalls       int
	refreshCalls       int
	lastRefreshDeploys int
}

func (p *refreshTrackingPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	p.planCalls++
	return &ExplorerPlan{
		ID:   "refresh-plan",
		Tier: 2,
		Tasks: []PlanTask{{
			Kind:     "validate",
			Model:    "test-model",
			Engine:   "vllm",
			Hardware: input.Hardware.Profile,
			Reason:   "test refresh",
		}},
	}, 0, nil
}

func (p *refreshTrackingPlanner) Analyze(ctx context.Context) (string, []TaskSpec, int, error) {
	p.analyzeCalls++
	return "done", nil, 0, nil
}

func (p *refreshTrackingPlanner) RefreshFacts(input PlanInput) error {
	p.refreshCalls++
	p.lastRefreshDeploys = len(input.ActiveDeploys)
	return nil
}

type pdcaBudgetPlanner struct {
	analyzeCalls int
}

func (p *pdcaBudgetPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	return &ExplorerPlan{
		ID:   "pdca-budget",
		Tier: 2,
		Tasks: []PlanTask{{
			Kind:     "validate",
			Model:    "seed-model",
			Engine:   "seed-engine",
			Hardware: "test-hw",
			Reason:   "seed task",
		}},
	}, 0, nil
}

func (p *pdcaBudgetPlanner) Analyze(ctx context.Context) (string, []TaskSpec, int, error) {
	p.analyzeCalls++
	return "continue", []TaskSpec{{
		Kind:   "validate",
		Model:  fmt.Sprintf("followup-%d", p.analyzeCalls),
		Engine: "seed-engine",
		Reason: "budget follow-up",
	}}, 0, nil
}

func TestExplorer_EmptyPlanCountsAsBudgetRound(t *testing.T) {
	bus := NewEventBus()
	calls := 0
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
	e.planner = &emptyPlanner{calls: &calls}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go e.Start(ctx)
	time.Sleep(10 * time.Millisecond)

	// Fire 5 events — empty plans should still count toward budget
	for i := 0; i < 5; i++ {
		bus.Publish(ExplorerEvent{Type: EventScheduledGapScan})
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)

	// Only 2 plans should be generated (maxRounds=2), even though they're empty
	if calls != 2 {
		t.Errorf("planner calls = %d, want 2 (empty plans should count toward budget)", calls)
	}
}

func TestExplorer_EmptyPlanPersistsBudgetRound(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	bus := NewEventBus()
	calls := 0
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	e := NewExplorer(ExplorerConfig{
		Schedule:  DefaultScheduleConfig(),
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 3,
	}, agent, nil, db, bus)
	e.planner = &emptyPlanner{calls: &calls}

	e.handleEvent(context.Background(), ExplorerEvent{Type: EventScheduledGapScan})

	got, err := db.GetConfig(context.Background(), "explorer.rounds_used")
	if err != nil {
		t.Fatalf("GetConfig explorer.rounds_used: %v", err)
	}
	if got != "1" {
		t.Fatalf("explorer.rounds_used = %q, want 1", got)
	}
}

func TestExplorerClaimPlanRound_BudgetModeCountsPDCAPlans(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	e := NewExplorer(ExplorerConfig{
		Schedule:  DefaultScheduleConfig(),
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 3,
	}, nil, nil, db, NewEventBus())

	for i := 1; i <= 3; i++ {
		got, ok := e.claimPlanRound(context.Background(), "budget", 3)
		if !ok {
			t.Fatalf("claim %d rejected unexpectedly", i)
		}
		if got != i {
			t.Fatalf("claim %d rounds_used=%d, want %d", i, got, i)
		}
	}

	got, err := db.GetConfig(context.Background(), "explorer.rounds_used")
	if err != nil {
		t.Fatalf("GetConfig explorer.rounds_used: %v", err)
	}
	if got != "3" {
		t.Fatalf("explorer.rounds_used = %q, want 3", got)
	}
}

func TestExplorerClaimPlanRound_BudgetModeRejectsFourthPlan(t *testing.T) {
	e := NewExplorer(ExplorerConfig{
		Schedule:  DefaultScheduleConfig(),
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 3,
	}, nil, nil, nil, NewEventBus())

	for i := 0; i < 3; i++ {
		if _, ok := e.claimPlanRound(context.Background(), "budget", 3); !ok {
			t.Fatalf("claim %d rejected unexpectedly", i+1)
		}
	}

	got, ok := e.claimPlanRound(context.Background(), "budget", 3)
	if ok {
		t.Fatal("fourth plan claim unexpectedly allowed")
	}
	if got != 3 {
		t.Fatalf("fourth plan rounds_used=%d, want 3", got)
	}
}

func TestExplorer_PDCAExtraPlansConsumeBudgetRounds(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	bus := NewEventBus()
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	planner := &pdcaBudgetPlanner{}
	e := NewExplorer(ExplorerConfig{
		Schedule:  DefaultScheduleConfig(),
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 3,
		MaxCycles: 4,
	}, agent, nil, db, bus,
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "test"}, nil
		}),
	)
	e.planner = planner

	e.handleEvent(context.Background(), ExplorerEvent{Type: EventScheduledGapScan})

	if planner.analyzeCalls != 3 {
		t.Fatalf("Analyze calls = %d, want 3 (third follow-up should hit budget wall)", planner.analyzeCalls)
	}

	got, err := db.GetConfig(context.Background(), "explorer.rounds_used")
	if err != nil {
		t.Fatalf("GetConfig explorer.rounds_used: %v", err)
	}
	if got != "3" {
		t.Fatalf("explorer.rounds_used = %q, want 3", got)
	}

	plans, err := db.ListExplorationPlans(context.Background(), "")
	if err != nil {
		t.Fatalf("ListExplorationPlans: %v", err)
	}
	if len(plans) != 3 {
		t.Fatalf("plans len = %d, want 3", len(plans))
	}

	planIDs := make(map[string]bool, len(plans))
	for _, plan := range plans {
		planIDs[plan.ID] = true
	}
	for _, want := range []string{"pdca-budget", "pdca-budget-c1", "pdca-budget-c2"} {
		if !planIDs[want] {
			t.Fatalf("missing persisted plan %q; got %v", want, planIDs)
		}
	}
	if planIDs["pdca-budget-c3"] {
		t.Fatalf("unexpected fourth plan persisted: %v", planIDs)
	}
}

func TestExplorer_EmptyPlanDisablesOnceModeInDB(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	bus := NewEventBus()
	calls := 0
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
		Mode:     "once",
	}, agent, nil, db, bus)
	e.planner = &emptyPlanner{calls: &calls}

	e.handleEvent(context.Background(), ExplorerEvent{Type: EventScheduledGapScan})

	got, err := db.GetConfig(context.Background(), "explorer.enabled")
	if err != nil {
		t.Fatalf("GetConfig explorer.enabled: %v", err)
	}
	if got != "false" {
		t.Fatalf("explorer.enabled = %q, want false", got)
	}
}

func TestExplorer_OnceModeDisablesAfterRound(t *testing.T) {
	bus := NewEventBus()
	plansExecuted := 0
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
		Mode:     "once",
	}, agent, nil, nil, bus,
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "test"}, nil
		}),
	)
	e.planner = &countingPlanner{executed: &plansExecuted}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go e.Start(ctx)
	time.Sleep(10 * time.Millisecond)
	bus.Publish(ExplorerEvent{Type: EventScheduledGapScan})
	time.Sleep(50 * time.Millisecond)

	if plansExecuted != 1 {
		t.Fatalf("plansExecuted = %d, want 1", plansExecuted)
	}
	if e.Status().Enabled {
		t.Fatal("expected once mode to disable immediately after the round finishes")
	}
}

func TestExplorer_RefreshesFactsBeforeAnalyze(t *testing.T) {
	bus := NewEventBus()
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	planner := &refreshTrackingPlanner{}
	deployCalls := 0

	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, agent, nil, nil, bus,
		WithGatherDeploys(func(ctx context.Context) ([]DeployStatus, error) {
			deployCalls++
			if deployCalls == 1 {
				return []DeployStatus{{Model: "old-model", Engine: "vllm", Status: "running"}}, nil
			}
			return []DeployStatus{
				{Model: "old-model", Engine: "vllm", Status: "running"},
				{Model: "new-model", Engine: "sglang-kt", Status: "running"},
			}, nil
		}),
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "Ada", GPUCount: 1, VRAMMiB: 24576}, nil
		}),
	)
	e.planner = planner

	e.handleEvent(context.Background(), ExplorerEvent{Type: EventScheduledGapScan})

	if planner.planCalls != 1 {
		t.Fatalf("planCalls = %d, want 1", planner.planCalls)
	}
	if planner.analyzeCalls != 1 {
		t.Fatalf("analyzeCalls = %d, want 1", planner.analyzeCalls)
	}
	if planner.refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", planner.refreshCalls)
	}
	if planner.lastRefreshDeploys != 2 {
		t.Fatalf("lastRefreshDeploys = %d, want 2", planner.lastRefreshDeploys)
	}
}

func TestExplorer_ReconcilesStaleActivePlansOnStart(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, nil, nil, db, bus)

	plan := &ExplorerPlan{
		ID:    "stale-start",
		Tier:  2,
		Tasks: []PlanTask{{Kind: "validate", Model: "m", Engine: "e", Hardware: "hw"}},
	}
	if err := e.persistExplorationPlan(context.Background(), plan, "manual"); err != nil {
		t.Fatalf("persistExplorationPlan: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Start(ctx)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	plans, err := db.ListExplorationPlans(context.Background(), "")
	if err != nil {
		t.Fatalf("ListExplorationPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans len=%d, want 1", len(plans))
	}
	if plans[0].Status != "cancelled" {
		t.Fatalf("plan status=%q, want cancelled", plans[0].Status)
	}
	if plans[0].CompletedAt == nil {
		t.Fatal("completed_at is nil, want reconciliation timestamp")
	}
}

func TestExplorer_ReconcilesStaleActivePlansDuringEvent(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	bus := NewEventBus()
	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, nil, nil, db, bus)

	plan := &ExplorerPlan{
		ID:    "stale-event",
		Tier:  2,
		Tasks: []PlanTask{{Kind: "validate", Model: "m", Engine: "e", Hardware: "hw"}},
	}
	if err := e.persistExplorationPlan(context.Background(), plan, "manual"); err != nil {
		t.Fatalf("persistExplorationPlan: %v", err)
	}

	e.handleEvent(context.Background(), ExplorerEvent{Type: EventScheduledGapScan})

	plans, err := db.ListExplorationPlans(context.Background(), "")
	if err != nil {
		t.Fatalf("ListExplorationPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans len=%d, want 1", len(plans))
	}
	if plans[0].Status != "cancelled" {
		t.Fatalf("plan status=%q, want cancelled", plans[0].Status)
	}
	if plans[0].CompletedAt == nil {
		t.Fatal("completed_at is nil, want reconciliation timestamp")
	}
}

func TestExplorer_ReconcileHistoricalExplorationRuns_UsesPlanSummaryAsCanonical(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	summaryJSON, err := json.Marshal(struct {
		Tasks []PlanTask
	}{
		Tasks: []PlanTask{{
			Kind:     "validate",
			Model:    "qwen3.5-27b",
			Engine:   "vllm",
			Status:   "failed",
			Reason:   "timed out",
			Priority: 1,
		}},
	})
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}

	now := time.Now()
	if err := db.InsertExplorationPlan(context.Background(), &state.ExplorationPlanRow{
		ID:        "plan-1",
		Tier:      2,
		Trigger:   "manual",
		Status:    "completed",
		PlanJSON:  `{"id":"plan-1"}`,
		Progress:  1,
		Total:     1,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("InsertExplorationPlan: %v", err)
	}
	if err := db.UpdateExplorationPlan(context.Background(), &state.ExplorationPlanRow{
		ID:          "plan-1",
		Status:      "completed",
		Progress:    1,
		CompletedAt: &now,
		SummaryJSON: string(summaryJSON),
	}); err != nil {
		t.Fatalf("UpdateExplorationPlan: %v", err)
	}

	if err := db.InsertExplorationRun(context.Background(), &state.ExplorationRun{
		ID:           "run-1",
		Kind:         "validate",
		Goal:         "[plan:plan-1] validate qwen3.5-27b on vllm",
		RequestedBy:  "explorer",
		Executor:     "go-agent",
		Planner:      "llm",
		Status:       "completed",
		HardwareID:   "nvidia-gb10-arm64",
		EngineID:     "vllm",
		ModelID:      "qwen3.5-27b",
		ApprovalMode: "auto",
		StartedAt:    now,
		CompletedAt:  now,
	}); err != nil {
		t.Fatalf("InsertExplorationRun: %v", err)
	}

	e := &Explorer{db: db}
	e.reconcileHistoricalExplorationRuns(context.Background())

	run, err := db.GetExplorationRun(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("GetExplorationRun: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("run status=%q, want failed", run.Status)
	}
	if !strings.Contains(run.Error, "reconciled from plan plan-1") {
		t.Fatalf("run error=%q, want reconciliation marker", run.Error)
	}

	combos, err := db.ListExploredCombos(context.Background())
	if err != nil {
		t.Fatalf("ListExploredCombos: %v", err)
	}
	if len(combos) != 1 {
		t.Fatalf("combos len=%d, want 1", len(combos))
	}
	if combos[0].Completed {
		t.Fatalf("combo completed=%v, want false", combos[0].Completed)
	}
	if combos[0].FailCount != 1 {
		t.Fatalf("combo fail_count=%d, want 1", combos[0].FailCount)
	}
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

func TestDefaultBenchmarkProfiles(t *testing.T) {
	tests := []struct {
		name             string
		hw               HardwareInfo
		wantLatencyCells int
		wantThroughput   bool
		wantMaxConc      int
	}{
		{"high_vram_98gb", HardwareInfo{VRAMMiB: 49000, GPUCount: 2}, 12, true, 8},
		{"medium_vram_24gb", HardwareInfo{VRAMMiB: 24000, GPUCount: 1}, 12, true, 4},
		{"low_vram_8gb", HardwareInfo{VRAMMiB: 8000, GPUCount: 1}, 4, false, 0},
		{"zero_vram", HardwareInfo{VRAMMiB: 0, GPUCount: 0}, 4, false, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			profiles := defaultBenchmarkProfiles(tc.hw)
			if len(profiles) == 0 {
				t.Fatal("no profiles returned")
			}
			latency := profiles[0]
			cells := len(latency.InputTokenLevels) * len(latency.MaxTokenLevels)
			if cells != tc.wantLatencyCells {
				t.Errorf("latency cells = %d, want %d", cells, tc.wantLatencyCells)
			}
			if tc.wantThroughput && len(profiles) < 2 {
				t.Error("expected throughput profile")
			}
			if tc.wantThroughput {
				maxConc := profiles[1].ConcurrencyLevels[len(profiles[1].ConcurrencyLevels)-1]
				if maxConc != tc.wantMaxConc {
					t.Errorf("max concurrency = %d, want %d", maxConc, tc.wantMaxConc)
				}
			}
		})
	}
}

func TestExplorerAgentPlanner_FilterTaskSpecs_GuardsBlockedAndNonReadyCombos(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	summaryMD := "# Exploration Summary\n\n" +
		"## Key Findings\n\n" +
		"- noop\n\n" +
		"## Bugs And Failures\n\n" +
		"- noop\n\n" +
		"## Confirmed Blockers\n" +
		"```yaml\n" +
		"- family: port_conflict\n" +
		"  scope: combo\n" +
		"  model: blocked-model\n" +
		"  engine: blocked-engine\n" +
		"  reason: blocked by active deployment\n" +
		"  retry_when: port is free\n" +
		"  confidence: confirmed\n" +
		"```\n\n" +
		"## Do Not Retry This Cycle\n" +
		"```yaml\n" +
		"- model: deny-model\n" +
		"  engine: deny-engine\n" +
		"  reason_family: runtime_busy\n" +
		"  reason: busy runtime\n" +
		"```\n\n" +
		"## Evidence Ledger\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Design Doubts\n\n" +
		"- noop\n\n" +
		"## Recommended Configurations\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Current Strategy\n\n" +
		"- noop\n\n" +
		"## Next Cycle Candidates\n\n" +
		"- noop\n"
	_ = ws.WriteFile("summary.md", summaryMD)

	planner := &ExplorerAgentPlanner{workspace: ws}
	input := PlanInput{
		ComboFacts: []ComboFact{
			{Model: "ready-model", Engine: "ready-engine", Status: "ready"},
			{Model: "blocked-model", Engine: "blocked-engine", Status: "blocked", Reason: "blocked by combo facts"},
		},
		SkipCombos: []SkipCombo{
			{Model: "skip-model", Engine: "skip-engine", Reason: "completed"},
		},
	}
	tasks := []TaskSpec{
		{Kind: "validate", Model: "ready-model", Engine: "ready-engine", Reason: "keep"},
		{Kind: "validate", Model: "blocked-model", Engine: "blocked-engine", Reason: "blocked"},
		{Kind: "validate", Model: "deny-model", Engine: "deny-engine", Reason: "deny"},
		{Kind: "validate", Model: "other-model", Engine: "other-engine", Reason: "other"},
		{Kind: "validate", Model: "skip-model", Engine: "skip-engine", Reason: "skip"},
	}

	filtered := planner.filterTaskSpecs(input, tasks)
	if len(filtered) != 1 {
		t.Fatalf("filtered len=%d, want 1", len(filtered))
	}
	if filtered[0].Model != "ready-model" || filtered[0].Engine != "ready-engine" {
		t.Fatalf("filtered task=%+v, want ready-model/ready-engine", filtered[0])
	}
}

func TestExtractMaxModelLen(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]any
		want   int
	}{
		{"nil params", nil, 0},
		{"empty params", map[string]any{}, 0},
		{"max_model_len float64", map[string]any{"max_model_len": float64(32768)}, 32768},
		{"context_length int", map[string]any{"context_length": 65536}, 65536},
		{"ctx_size", map[string]any{"ctx_size": float64(4096)}, 4096},
		{"prefers max_model_len over context_length", map[string]any{"max_model_len": float64(8192), "context_length": float64(32768)}, 8192},
		{"ignores non-numeric", map[string]any{"max_model_len": "auto"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractMaxModelLen(tt.params)
			if got != tt.want {
				t.Errorf("extractMaxModelLen() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAdaptBenchmarkProfiles(t *testing.T) {
	base := []ExplorationBenchmarkProfile{
		{
			Label:             "throughput",
			ConcurrencyLevels: []int{1, 4, 8},
			InputTokenLevels:  []int{128, 512, 1024, 2048, 4096, 8192},
			MaxTokenLevels:    []int{128, 512},
			RequestsPerCombo:  3,
		},
		{
			Label:             "latency",
			ConcurrencyLevels: []int{1},
			InputTokenLevels:  []int{128, 512, 1024, 2048},
			MaxTokenLevels:    []int{256},
			RequestsPerCombo:  5,
		},
	}

	t.Run("bounded enrichment keeps planner intent", func(t *testing.T) {
		sparse := []ExplorationBenchmarkProfile{{
			Label:             "plan",
			ConcurrencyLevels: []int{1, 4},
			InputTokenLevels:  []int{512},
			MaxTokenLevels:    []int{128},
			RequestsPerCombo:  3,
		}}
		result := adaptBenchmarkProfiles(sparse, 32768)
		got := result[0].InputTokenLevels
		want := []int{128, 512, 2048}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("InputTokenLevels = %v, want %v", got, want)
		}
	})

	t.Run("does not explode to full ladder for large context models", func(t *testing.T) {
		result := adaptBenchmarkProfiles(base, 32768)
		if !reflect.DeepEqual(result[0].InputTokenLevels, base[0].InputTokenLevels) {
			t.Fatalf("InputTokenLevels = %v, want original planner/profile levels %v", result[0].InputTokenLevels, base[0].InputTokenLevels)
		}
	})

	t.Run("filters infeasible max_tokens", func(t *testing.T) {
		small := []ExplorationBenchmarkProfile{{
			InputTokenLevels: []int{128},
			MaxTokenLevels:   []int{256, 2048, 8192},
		}}
		result := adaptBenchmarkProfiles(small, 4096)
		for _, mt := range result[0].MaxTokenLevels {
			if mt == 8192 {
				t.Error("expected max_tokens 8192 to be filtered for 4096 context model")
			}
		}
		if len(result[0].MaxTokenLevels) != 2 {
			t.Errorf("expected 2 feasible max_tokens, got %v", result[0].MaxTokenLevels)
		}
	})

	t.Run("no-op for zero maxModelLen", func(t *testing.T) {
		result := adaptBenchmarkProfiles(base, 0)
		if len(result[0].InputTokenLevels) != len(base[0].InputTokenLevels) {
			t.Error("expected no change for maxModelLen=0")
		}
	})

	t.Run("very small model keeps at least one level", func(t *testing.T) {
		result := adaptBenchmarkProfiles(base, 256)
		if len(result[0].InputTokenLevels) == 0 {
			t.Error("expected at least one input level even for tiny model")
		}
		if result[0].InputTokenLevels[0] > 256 {
			t.Errorf("expected first level to fit max context, got %d", result[0].InputTokenLevels[0])
		}
	})
}

func TestTaskSpecFromPlanTaskPreservesBenchmark(t *testing.T) {
	task := PlanTask{
		Kind:   "validate",
		Model:  "Qwen2.5-Coder-3B-Instruct",
		Engine: "sglang",
		Params: map[string]any{"max_model_len": float64(8192)},
		Benchmark: BenchmarkSpec{
			Concurrency:      []int{1, 4},
			InputTokens:      []int{512},
			MaxTokens:        []int{128},
			RequestsPerCombo: 3,
		},
		Reason: "preserve planner-authored benchmark",
	}

	got := taskSpecFromPlanTask(task)
	if !reflect.DeepEqual(got.Benchmark, task.Benchmark) {
		t.Fatalf("Benchmark = %#v, want %#v", got.Benchmark, task.Benchmark)
	}
}

func TestExplorerParseExplorationResult_PreservesArtifactsAndMatrix(t *testing.T) {
	explorer := &Explorer{}
	status := &ExplorationStatus{
		Run: &state.ExplorationRun{
			SummaryJSON: `{
				"benchmark_id":"bench-001",
				"config_id":"cfg-001",
				"engine_version":"1.2.3",
				"engine_image":"example/vllm:1.2.3",
				"resource_usage":{"vram_usage_mib":1234},
				"deploy_config":{"tensor_parallel_size":2},
				"result":{"throughput_tps":95.2,"ttft_p95_ms":42,"tpot_p95_ms":118},
				"matrix_profiles":[{"label":"latency","cells":[{"concurrency":1,"input_tokens":128,"max_tokens":256,"benchmark_id":"bench-001","config_id":"cfg-001","engine_version":"1.2.3","engine_image":"example/vllm:1.2.3","resource_usage":{"vram_usage_mib":1234},"result":{"throughput_tps":95.2,"ttft_p95_ms":42,"tpot_p95_ms":118}}]}],
				"total_cells":1,
				"success_cells":1
			}`,
		},
	}

	result := explorer.parseExplorationResult(status)
	if result.BenchmarkID != "bench-001" || result.ConfigID != "cfg-001" {
		t.Fatalf("artifacts not preserved: %+v", result)
	}
	if result.EngineImage != "example/vllm:1.2.3" || result.EngineVersion != "1.2.3" {
		t.Fatalf("engine metadata not preserved: %+v", result)
	}
	if result.MatrixCells != 1 || result.SuccessCells != 1 {
		t.Fatalf("matrix counts = (%d,%d), want (1,1)", result.MatrixCells, result.SuccessCells)
	}
	if !strings.Contains(result.MatrixJSON, "matrix_profiles") || !strings.Contains(result.MatrixJSON, "bench-001") {
		t.Fatalf("matrix JSON missing propagated artifacts: %s", result.MatrixJSON)
	}
	if got := result.ResourceUsage["vram_usage_mib"]; got != float64(1234) {
		t.Fatalf("resource usage = %#v, want 1234", got)
	}
	if got := result.DeployConfig["tensor_parallel_size"]; got != float64(2) {
		t.Fatalf("deploy config = %#v, want 2", got)
	}
}

func TestExplorerAgentPlanner_AnalyzeRejectsInvalidValidatedConfidence(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	validSummary := "# Exploration Summary\n\n" +
		"## Key Findings\n\n" +
		"- benchmark evidence exists\n\n" +
		"## Bugs And Failures\n\n" +
		"- none\n\n" +
		"## Confirmed Blockers\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Do Not Retry This Cycle\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Evidence Ledger\n" +
		"```yaml\n" +
		"- source: this_cycle\n" +
		"  kind: benchmark\n" +
		"  model: test-model\n" +
		"  engine: vllm\n" +
		"  evidence: benchmark run\n" +
		"  summary: validated against matrix\n" +
		"  confidence: high\n" +
		"```\n\n" +
		"## Design Doubts\n\n" +
		"- none\n\n" +
		"## Recommended Configurations\n" +
		"```yaml\n" +
		"- model: test-model\n" +
		"  engine: vllm\n" +
		"  hardware: test-hw\n" +
		"  engine_params: {}\n" +
		"  performance:\n" +
		"    throughput_tps: 120.0\n" +
		"    latency_p50_ms: 35\n" +
		"  confidence: validated\n" +
		"  note: \"ok\"\n" +
		"```\n\n" +
		"## Current Strategy\n\n" +
		"- keep going\n\n" +
		"## Next Cycle Candidates\n\n" +
		"- none\n"
	invalidSummary := "# Exploration Summary\n\n" +
		"## Key Findings\n\n" +
		"- benchmark evidence missing\n\n" +
		"## Bugs And Failures\n\n" +
		"- none\n\n" +
		"## Confirmed Blockers\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Do Not Retry This Cycle\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Evidence Ledger\n" +
		"```yaml\n" +
		"[]\n" +
		"```\n\n" +
		"## Design Doubts\n\n" +
		"- none\n\n" +
		"## Recommended Configurations\n" +
		"```yaml\n" +
		"- model: test-model\n" +
		"  engine: vllm\n" +
		"  hardware: test-hw\n" +
		"  engine_params: {}\n" +
		"  performance:\n" +
		"    throughput_tps: 0\n" +
		"    latency_p50_ms: 0\n" +
		"  confidence: validated\n" +
		"  note: \"too strong\"\n" +
		"```\n\n" +
		"## Current Strategy\n\n" +
		"- keep going\n\n" +
		"## Next Cycle Candidates\n\n" +
		"- none\n"

	cases := []struct {
		name         string
		summary      string
		wantErr      bool
		wantFeedback string
	}{
		{
			name:         "validated_with_benchmark_evidence",
			summary:      validSummary,
			wantErr:      false,
			wantFeedback: "",
		},
		{
			name:         "validated_without_benchmark_evidence",
			summary:      invalidSummary,
			wantErr:      false,
			wantFeedback: "validated without benchmark evidence",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ws.WriteFile("summary.md", tc.summary); err != nil {
				t.Fatalf("WriteFile summary.md: %v", err)
			}
			mock := &mockStreamingLLM{
				responses: []Response{{Content: tc.summary}},
			}
			planner := NewExplorerAgentPlanner(mock, ws)
			_, _, _, err := planner.Analyze(context.Background())
			if err != nil {
				t.Fatalf("Analyze() unexpected error: %v", err)
			}
			// Validation guard now injects feedback into workspace instead of returning error
			if tc.wantFeedback != "" {
				content, readErr := ws.ReadFile("summary.md")
				if readErr != nil {
					t.Fatalf("ReadFile summary.md: %v", readErr)
				}
				if !strings.Contains(content, "Validation Guard Feedback") {
					t.Fatal("expected validation guard feedback in summary.md")
				}
				if !strings.Contains(content, tc.wantFeedback) {
					t.Fatalf("summary.md missing expected feedback %q", tc.wantFeedback)
				}
			}
		})
	}
}

func TestExplorerExecutePlan_FinalizesCancelledPlan(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}

	e := &Explorer{
		db:        db,
		workspace: ws,
		harvester: NewHarvester(1),
	}

	plan := &ExplorerPlan{
		ID:   "plan-1",
		Tier: 2,
		Tasks: []PlanTask{
			{Kind: "validate", Model: "model-a", Engine: "engine-a", Hardware: "hw-a", Reason: "test"},
		},
	}
	if err := e.persistExplorationPlan(context.Background(), plan, "test-trigger"); err != nil {
		t.Fatalf("persistExplorationPlan: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e.executePlan(ctx, plan)

	plans, err := db.ListExplorationPlans(context.Background(), "")
	if err != nil {
		t.Fatalf("ListExplorationPlans: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("plans len=%d, want 1", len(plans))
	}
	if plans[0].Status != "cancelled" {
		t.Fatalf("plan status=%q, want cancelled", plans[0].Status)
	}
	if plans[0].CompletedAt == nil {
		t.Fatal("completed_at is nil, want terminal timestamp")
	}
	if plans[0].Progress != 1 {
		t.Fatalf("plan progress=%d, want 1", plans[0].Progress)
	}
	if plan.Tasks[0].Status != "skipped_timeout" {
		t.Fatalf("task status=%q, want skipped_timeout", plan.Tasks[0].Status)
	}
	if got, err := ws.ReadFile("experiments/001-model-a-engine-a.md"); err != nil {
		t.Fatalf("ReadFile experiment: %v", err)
	} else if !containsAll(got, "skipped_timeout", "model-a", "engine-a") {
		t.Fatalf("experiment artifact missing timeout status: %q", got)
	}
}

func TestExtractRepresentativeCell(t *testing.T) {
	tests := []struct {
		name      string
		json      string
		wantOK    bool
		wantInput int // expected input_tokens of chosen cell
	}{
		{
			"prefers_conc1_near_1024",
			`{"matrix_profiles":[{"label":"latency","cells":[
				{"concurrency":1,"input_tokens":128,"max_tokens":256,"result":{"throughput_tps":170}},
				{"concurrency":1,"input_tokens":1024,"max_tokens":1024,"result":{"throughput_tps":155}},
				{"concurrency":4,"input_tokens":1024,"max_tokens":1024,"result":{"throughput_tps":520}}
			]}]}`,
			true, 1024,
		},
		{
			"empty_matrix",
			`{"matrix_profiles":[]}`,
			false, 0,
		},
		{
			"all_errors",
			`{"matrix_profiles":[{"label":"latency","cells":[
				{"concurrency":1,"input_tokens":1024,"max_tokens":1024,"error":"timeout"}
			]}]}`,
			false, 0,
		},
		{
			"single_cell",
			`{"matrix_profiles":[{"label":"latency","cells":[
				{"concurrency":2,"input_tokens":512,"max_tokens":256,"result":{"throughput_tps":100}}
			]}]}`,
			true, 512,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cell, ok := extractRepresentativeCell(tc.json)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			var gotInput int
			switch v := cell["input_tokens"].(type) {
			case int:
				gotInput = v
			case float64:
				gotInput = int(v)
			}
			if gotInput != tc.wantInput {
				t.Errorf("input_tokens = %v, want %d", cell["input_tokens"], tc.wantInput)
			}
		})
	}
}
