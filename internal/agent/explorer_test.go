package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	reflect "reflect"
	"sort"
	"strings"
	"sync/atomic"
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
	var plansExecuted atomic.Int32
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

	if got := plansExecuted.Load(); got != 2 {
		t.Errorf("plansExecuted = %d, want 2 (maxRounds)", got)
	}
}

// countingPlanner is a test planner that generates 1-task plans and counts invocations.
type countingPlanner struct {
	executed *atomic.Int32
}

func (p *countingPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	n := p.executed.Add(1)
	return &ExplorerPlan{
		ID:    fmt.Sprintf("test-%d", n),
		Tier:  1,
		Tasks: []PlanTask{{Kind: "validate", Model: "m", Engine: "e", Priority: 0}},
	}, 0, nil
}

// emptyPlanner always returns 0-task plans (simulates all tasks deduped post-hoc).
type emptyPlanner struct {
	calls *atomic.Int32
}

func (p *emptyPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	n := p.calls.Add(1)
	return &ExplorerPlan{ID: fmt.Sprintf("empty-%d", n), Tier: 2, Tasks: nil}, 100, nil
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
	var calls atomic.Int32
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
	if got := calls.Load(); got != 2 {
		t.Errorf("planner calls = %d, want 2 (empty plans should count toward budget)", got)
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
	var calls atomic.Int32
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
	metrics := e.Status().LastPlanMetrics
	if metrics == nil {
		t.Fatal("LastPlanMetrics is nil")
	}
	if metrics.TotalTasks != 3 || metrics.Completed != 0 || metrics.Failed != 3 {
		t.Fatalf("LastPlanMetrics = %+v, want total=3 completed=0 failed=3", metrics)
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
	var calls atomic.Int32
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
	var plansExecuted atomic.Int32
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

	if got := plansExecuted.Load(); got != 1 {
		t.Fatalf("plansExecuted = %d, want 1", got)
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

func TestParseAdvisoryTaskCarriesScalarConfigAndHardware(t *testing.T) {
	taskInfo, task, err := parseAdvisoryTask(json.RawMessage(`{
		"id":"adv-1",
		"type":"recommendation",
		"target_model":"qwen3-8b",
		"target_engine":"vllm",
		"content_json":{
			"gpu_memory_utilization":0.8,
			"max_model_len":4096,
			"enable_prefix_caching":true,
			"nested":{"bad":"value"},
			"list":[1,2,3]
		}
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
	if task.Params["max_model_len"] != 4096.0 {
		t.Fatalf("params = %v, want max_model_len", task.Params)
	}
	if task.Params["enable_prefix_caching"] != true {
		t.Fatalf("params = %v, want enable_prefix_caching", task.Params)
	}
	if _, ok := task.Params["nested"]; ok {
		t.Fatalf("params = %v, want nested config dropped", task.Params)
	}
	if _, ok := task.Params["list"]; ok {
		t.Fatalf("params = %v, want list config dropped", task.Params)
	}
	if task.SourceRef != "adv-1" {
		t.Fatalf("source_ref = %q, want adv-1", task.SourceRef)
	}
}

func TestTaskBlockedByExecutableFacts(t *testing.T) {
	tests := []struct {
		name       string
		model      string
		engine     string
		comboFacts []ComboFact
		skipCombos []SkipCombo
		wantReason string
		wantBlock  bool
	}{
		{
			name:       "ready combo allowed",
			model:      "qwen3-8b",
			engine:     "vllm",
			comboFacts: []ComboFact{{Model: "qwen3-8b", Engine: "vllm", Status: "ready"}},
			wantBlock:  false,
		},
		{
			name:       "blocked combo rejected",
			model:      "qwen3-8b",
			engine:     "vllm",
			comboFacts: []ComboFact{{Model: "qwen3-8b", Engine: "vllm", Status: "blocked", Reason: "image missing required op"}},
			wantReason: "image missing required op",
			wantBlock:  true,
		},
		{
			name:       "combo outside ready set rejected",
			model:      "qwen3-8b",
			engine:     "vllm",
			comboFacts: []ComboFact{{Model: "glm-4.1v", Engine: "sglang", Status: "ready"}},
			wantReason: "not in ready combos",
			wantBlock:  true,
		},
		{
			name:       "skip combo reason wins",
			model:      "qwen3-8b",
			engine:     "vllm",
			skipCombos: []SkipCombo{{Model: "qwen3-8b", Engine: "vllm", Reason: "already explored in this round"}},
			wantReason: "already explored in this round",
			wantBlock:  true,
		},
		{
			name:       "model scope skip wins",
			model:      "qwen3-8b",
			engine:     "sglang",
			skipCombos: []SkipCombo{{Model: "qwen3-8b", Reason: "summary blocker: no viable engine remains"}},
			wantReason: "summary blocker: no viable engine remains",
			wantBlock:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reason, blocked := taskBlockedByExecutableFacts(tc.model, tc.engine, tc.comboFacts, tc.skipCombos)
			if blocked != tc.wantBlock {
				t.Fatalf("blocked = %v, want %v (reason=%q)", blocked, tc.wantBlock, reason)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func TestExplorerHandleEvent_RejectsBudgetExhaustedAdvisory(t *testing.T) {
	bus := NewEventBus()
	var gotID, gotStatus, gotReason string
	e := NewExplorer(ExplorerConfig{
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 1,
		Schedule:  DefaultScheduleConfig(),
	}, nil, nil, nil, bus,
		WithRoundsUsed(1),
		WithAdvisoryFeedback(func(ctx context.Context, advisoryID, status, reason string) error {
			gotID, gotStatus, gotReason = advisoryID, status, reason
			return nil
		}),
	)
	e.tokenResetDate = time.Now().Format("2006-01-02")

	e.handleEvent(context.Background(), ExplorerEvent{
		Type: EventCentralAdvisory,
		Advisory: json.RawMessage(`{
			"id":"adv-budget",
			"type":"recommendation",
			"target_model":"qwen3-8b",
			"target_engine":"vllm"
		}`),
	})

	if gotID != "adv-budget" {
		t.Fatalf("advisory id = %q, want adv-budget", gotID)
	}
	if gotStatus != "rejected" {
		t.Fatalf("status = %q, want rejected", gotStatus)
	}
	if !strings.Contains(gotReason, "budget exhausted") {
		t.Fatalf("reason = %q, want budget exhausted", gotReason)
	}
	if phase := e.Status().Phase; phase != "budget_exhausted" {
		t.Fatalf("phase = %q, want budget_exhausted", phase)
	}
}

func TestAdvisoryPlanStatus(t *testing.T) {
	tests := []struct {
		name string
		in   HarvestResult
		want string
	}{
		{name: "success", in: HarvestResult{Success: true}, want: "completed"},
		{name: "cancelled", in: HarvestResult{Cancelled: true}, want: "cancelled"},
		{name: "failed validation becomes rejected", in: HarvestResult{Success: false, Error: "boom"}, want: "rejected"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := advisoryPlanStatus(tc.in); got != tc.want {
				t.Fatalf("advisoryPlanStatus(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExplorerAdvisoryTaskAllowed(t *testing.T) {
	t.Run("rejects combo outside ready set", func(t *testing.T) {
		e := &Explorer{
			gatherHardware: func(ctx context.Context) (HardwareInfo, error) {
				return HardwareInfo{Profile: "nvidia-gb10-arm64", GPUArch: "blackwell"}, nil
			},
			gatherLocalModels: func(ctx context.Context) ([]LocalModel, error) {
				return []LocalModel{{Name: "qwen3-8b", Type: "llm"}}, nil
			},
			gatherLocalEngines: func(ctx context.Context) ([]LocalEngine, error) {
				return []LocalEngine{{Name: "sglang", Type: "sglang"}}, nil
			},
			gatherComboFacts: func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error) {
				return []ComboFact{{Model: "glm-4.1v", Engine: "sglang", Status: "ready"}}, nil
			},
		}

		ok, reason := e.advisoryTaskAllowed(context.Background(), PlanTask{Model: "qwen3-8b", Engine: "vllm"})
		if ok {
			t.Fatal("expected advisory task to be rejected")
		}
		if reason != "not in ready combos" {
			t.Fatalf("reason = %q, want not in ready combos", reason)
		}
	})

	t.Run("rejects completed history", func(t *testing.T) {
		db, err := state.Open(context.Background(), ":memory:")
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer db.Close()
		now := time.Now()
		if err := db.InsertExplorationRun(context.Background(), &state.ExplorationRun{
			ID:           "run-completed",
			Kind:         "validate",
			Goal:         "validate qwen3-8b on vllm",
			RequestedBy:  "explorer",
			Executor:     "go-agent",
			Planner:      "llm",
			Status:       "completed",
			HardwareID:   "nvidia-gb10-arm64",
			EngineID:     "vllm",
			ModelID:      "qwen3-8b",
			ApprovalMode: "auto",
			StartedAt:    now,
			CompletedAt:  now,
		}); err != nil {
			t.Fatalf("InsertExplorationRun: %v", err)
		}
		e := &Explorer{db: db}

		ok, reason := e.advisoryTaskAllowed(context.Background(), PlanTask{Model: "qwen3-8b", Engine: "vllm"})
		if ok {
			t.Fatal("expected advisory task to be rejected")
		}
		if reason != "already completed on this device" {
			t.Fatalf("reason = %q, want already completed on this device", reason)
		}
	})
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

func TestEffectiveTaskMaxModelLen(t *testing.T) {
	model := &LocalModel{Name: "qwen3-8b", MaxContextLen: 32768}
	if got := effectiveTaskMaxModelLen(PlanTask{Params: map[string]any{"max_model_len": float64(8192)}}, model); got != 8192 {
		t.Fatalf("effectiveTaskMaxModelLen override = %d, want 8192", got)
	}
	if got := effectiveTaskMaxModelLen(PlanTask{}, model); got != 32768 {
		t.Fatalf("effectiveTaskMaxModelLen fallback = %d, want 32768", got)
	}
	if got := effectiveTaskMaxModelLen(PlanTask{}, nil); got != 0 {
		t.Fatalf("effectiveTaskMaxModelLen nil model = %d, want 0", got)
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
		want := []int{128, 512, 16384}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("InputTokenLevels = %v, want %v", got, want)
		}
	})

	t.Run("does not explode to full ladder for large context models", func(t *testing.T) {
		result := adaptBenchmarkProfiles(base, 32768)
		want := []int{128, 512, 1024, 2048, 4096, 8192, 16384}
		if !reflect.DeepEqual(result[0].InputTokenLevels, want) {
			t.Fatalf("InputTokenLevels = %v, want bounded ladder with one high anchor %v", result[0].InputTokenLevels, want)
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

func TestTaskSpecFromPlanTaskPreservesSearchSpace(t *testing.T) {
	task := PlanTask{
		Kind:   "tune",
		Model:  "Qwen2.5-Coder-3B-Instruct",
		Engine: "sglang",
		SearchSpace: map[string][]any{
			"mem_fraction_static": []any{0.75, 0.85, 0.9},
			"max_model_len":       []any{8192, 16384},
		},
		Reason: "preserve tune search space",
	}

	got := taskSpecFromPlanTask(task)
	if !reflect.DeepEqual(got.SearchSpace, task.SearchSpace) {
		t.Fatalf("SearchSpace = %#v, want %#v", got.SearchSpace, task.SearchSpace)
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

func TestExplorerParseExplorationResult_ParsesTuningSummary(t *testing.T) {
	explorer := &Explorer{}
	status := &ExplorationStatus{
		Run: &state.ExplorationRun{
			SummaryJSON: `{
				"benchmark_id":"bench-123",
				"config_id":"cfg-123",
				"engine_version":"1.0.0",
				"engine_image":"example/sglang:1.0.0",
				"deploy_config":{"mem_fraction_static":0.8},
				"resource_usage":{"vram_usage_mib":2048},
				"result":{
					"throughput_tps":88.8,
					"ttft_p50_ms":100.1,
					"ttft_p95_ms":120.2,
					"tpot_p50_ms":10.3,
					"tpot_p95_ms":12.4,
					"config":{"concurrency":1,"input_tokens":512,"max_tokens":128}
				},
				"matrix_profiles":[
					{"label":"mem_fraction_static=0.8","cells":[
						{"concurrency":1,"input_tokens":512,"max_tokens":128,"benchmark_id":"bench-123","config_id":"cfg-123","engine_version":"1.0.0","engine_image":"example/sglang:1.0.0","resource_usage":{"vram_usage_mib":2048},"result":{"throughput_tps":88.8,"ttft_p95_ms":120.2,"tpot_p95_ms":12.4}}
					]}
				],
				"total_cells":1,
				"success_cells":1
			}`,
		},
	}

	result := explorer.parseExplorationResult(status)
	if result.BenchmarkID != "bench-123" || result.ConfigID != "cfg-123" {
		t.Fatalf("artifacts not preserved: %+v", result)
	}
	if result.Throughput != 88.8 || result.TTFTP50 != 100.1 || result.TPOTP95 != 12.4 {
		t.Fatalf("metrics not preserved: %+v", result)
	}
	if result.Concurrency != 1 || result.InputTokens != 512 || result.MaxTokens != 128 {
		t.Fatalf("config not preserved: %+v", result)
	}
	if result.MatrixCells != 1 || result.SuccessCells != 1 || !strings.Contains(result.MatrixJSON, "mem_fraction_static=0.8") {
		t.Fatalf("matrix not preserved: %+v", result)
	}
}

// TestExplorerParseExplorationResult_PreservesPartialMatrix is the core
// regression test for the tune-timeout narrative fix: when a tune is cut
// short at the 30m cap after some cells have already landed in DB,
// summarizeTuningSession writes partial matrix counts into SummaryJSON
// (total_cells > success_cells). parseExplorationResult must surface that
// truthfully so executeTask's timeout branch preserves it instead of
// reporting cells=0/0.
func TestExplorerParseExplorationResult_PreservesPartialMatrix(t *testing.T) {
	explorer := &Explorer{}
	status := &ExplorationStatus{
		Run: &state.ExplorationRun{
			SummaryJSON: `{
				"benchmark_id":"bench-partial",
				"config_id":"cfg-partial",
				"deploy_config":{"gpu_memory_utilization":0.8},
				"result":{"throughput_tps":22.5},
				"matrix_profiles":[
					{"label":"gmu=0.7","cells":[{"concurrency":1,"input_tokens":128,"max_tokens":256,"benchmark_id":"bench-0","config_id":"cfg-0","result":{"throughput_tps":20.1}}]},
					{"label":"gmu=0.8","cells":[{"concurrency":1,"input_tokens":128,"max_tokens":256,"benchmark_id":"bench-1","config_id":"cfg-1","result":{"throughput_tps":22.5}}]}
				],
				"total_cells":5,
				"success_cells":2
			}`,
		},
	}

	result := explorer.parseExplorationResult(status)
	if result.MatrixCells != 5 {
		t.Fatalf("MatrixCells = %d, want 5 (planned cells)", result.MatrixCells)
	}
	if result.SuccessCells != 2 {
		t.Fatalf("SuccessCells = %d, want 2 (landed cells before timeout)", result.SuccessCells)
	}
	if result.BenchmarkID != "bench-partial" {
		t.Fatalf("BenchmarkID = %q, want bench-partial", result.BenchmarkID)
	}
	if !strings.Contains(result.MatrixJSON, "gmu=0.7") || !strings.Contains(result.MatrixJSON, "gmu=0.8") {
		t.Fatalf("MatrixJSON dropped partial cells: %s", result.MatrixJSON)
	}
	// Success is set by the caller based on framework status, not by this
	// parser — but initializing to true here is what lets the timeout
	// branch in executeTask keep Cancelled=true while retaining cells.
	if !result.Success {
		t.Fatalf("Success initial value = false, want true (executeTask flips it on cancelled)")
	}
}

func TestExplorerAgentPlanner_AnalyzeRejectsInvalidValidatedConfidence(t *testing.T) {
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
		"    latency_scenario: \"concurrency=1, input=128, max_tokens=256\"\n" +
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
		name            string
		summary         string
		addExperiment   bool
		experimentModel string
		wantErr         bool
		wantFeedback    string
	}{
		{
			name:            "validated_with_benchmark_evidence",
			summary:         validSummary,
			addExperiment:   true,
			experimentModel: "test-model",
			wantErr:         false,
			wantFeedback:    "",
		},
		{
			name: "validated_with_zero_latency_but_matching_success_experiment",
			summary: strings.ReplaceAll(validSummary,
				"    latency_p50_ms: 35\n",
				"    latency_p50_ms: 0\n"),
			addExperiment:   true,
			experimentModel: "test-model",
			wantErr:         false,
			wantFeedback:    "",
		},
		{
			name: "validated_with_unanchored_latency_proxy",
			summary: strings.ReplaceAll(validSummary,
				"    latency_p50_ms: 35\n    latency_scenario: \"concurrency=1, input=128, max_tokens=256\"\n",
				"    latency_p50_ms: 9999\n    latency_scenario: \"concurrency=1, input=999, max_tokens=999\"\n"),
			addExperiment:   true,
			experimentModel: "test-model",
			wantErr:         false,
			wantFeedback:    "latency is not grounded by a matching benchmark scenario",
		},
		{
			name:            "validated_without_benchmark_evidence",
			summary:         invalidSummary,
			addExperiment:   false,
			experimentModel: "",
			wantErr:         false,
			wantFeedback:    "validated without benchmark evidence",
		},
		{
			name:            "validated_without_matching_experiment",
			summary:         validSummary,
			addExperiment:   true,
			experimentModel: "other-model",
			wantErr:         false,
			wantFeedback:    "validated without matching successful experiment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			ws := NewExplorerWorkspace(dir)
			_ = ws.Init()
			if err := ws.WriteFile("summary.md", tc.summary); err != nil {
				t.Fatalf("WriteFile summary.md: %v", err)
			}
			if tc.addExperiment {
				if _, err := ws.WriteExperimentResult(1, TaskSpec{
					Kind:   "validate",
					Model:  tc.experimentModel,
					Engine: "vllm",
				}, ExperimentResult{
					Status: "completed",
					Benchmarks: []BenchmarkEntry{{
						Concurrency:   1,
						InputTokens:   128,
						MaxTokens:     256,
						ThroughputTPS: 120.0,
						TTFTP50Ms:     10,
						TTFTP95Ms:     35,
						TPOTP50Ms:     0.10,
					}},
				}); err != nil {
					t.Fatalf("WriteExperimentResult: %v", err)
				}
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

func TestExplorerAgentPlannerFilterTaskSpecs_RebalancesCoverage(t *testing.T) {
	planner := &ExplorerAgentPlanner{maxTasks: 2}
	input := PlanInput{
		LocalModels: []LocalModel{
			{Name: "qwen3-8b", Family: "qwen"},
			{Name: "GLM-4.6V-Flash-FP4", Family: "glm"},
		},
		History: []ExplorationRun{
			{ModelID: "qwen3-8b", EngineID: "vllm", Status: "failed"},
		},
		ComboFacts: []ComboFact{
			{Model: "qwen3-8b", Engine: "vllm", Status: "ready"},
			{Model: "qwen3-8b", Engine: "sglang", Status: "ready"},
			{Model: "GLM-4.6V-Flash-FP4", Engine: "vllm-nightly", Status: "ready"},
		},
	}
	tasks := []TaskSpec{
		{Kind: "validate", Model: "qwen3-8b", Engine: "vllm"},
		{Kind: "validate", Model: "qwen3-8b", Engine: "sglang"},
		{Kind: "validate", Model: "GLM-4.6V-Flash-FP4", Engine: "vllm-nightly"},
	}

	filtered := planner.filterTaskSpecs(input, tasks)
	if len(filtered) != 2 {
		t.Fatalf("filtered len=%d, want 2", len(filtered))
	}
	if filtered[0].Model != "GLM-4.6V-Flash-FP4" {
		t.Fatalf("first task model=%q, want unexplored GLM first", filtered[0].Model)
	}
	if filtered[1].Model != "qwen3-8b" {
		t.Fatalf("second task model=%q, want qwen3-8b", filtered[1].Model)
	}
}

func TestExplorerAgentPlannerFilterTaskSpecs_AvoidsDuplicateModelWhenAlternativesExist(t *testing.T) {
	planner := &ExplorerAgentPlanner{maxTasks: 2}
	input := PlanInput{
		LocalModels: []LocalModel{
			{Name: "qwen3-8b", Family: "qwen"},
			{Name: "glm-4.1v", Family: "glm"},
		},
		ComboFacts: []ComboFact{
			{Model: "qwen3-8b", Engine: "vllm", Status: "ready"},
			{Model: "qwen3-8b", Engine: "sglang", Status: "ready"},
			{Model: "glm-4.1v", Engine: "sglang", Status: "ready"},
		},
	}
	tasks := []TaskSpec{
		{Kind: "validate", Model: "qwen3-8b", Engine: "vllm"},
		{Kind: "validate", Model: "qwen3-8b", Engine: "sglang"},
		{Kind: "validate", Model: "glm-4.1v", Engine: "sglang"},
	}

	filtered := planner.filterTaskSpecs(input, tasks)
	if len(filtered) != 2 {
		t.Fatalf("filtered len=%d, want 2", len(filtered))
	}
	if filtered[0].Model == filtered[1].Model {
		t.Fatalf("filtered tasks = %+v, want distinct models while alternatives exist", filtered)
	}
}

func TestExplorerAgentPlannerFilterTaskSpecs_PrioritizesPendingWork(t *testing.T) {
	planner := &ExplorerAgentPlanner{maxTasks: 2}
	input := PlanInput{
		LocalModels: []LocalModel{
			{Name: "qwen3-8b", Family: "qwen"},
			{Name: "glm-4.1v", Family: "glm"},
		},
		History: []ExplorationRun{
			{ModelID: "qwen3-8b", EngineID: "vllm", Status: "completed"},
		},
		ComboFacts: []ComboFact{
			{Model: "qwen3-8b", Engine: "vllm", Status: "ready"},
			{Model: "glm-4.1v", Engine: "sglang", Status: "ready"},
		},
		PendingWork: []PendingWork{
			{Model: "qwen3-8b", Engine: "vllm", Kind: "validate_long_context"},
		},
	}
	tasks := []TaskSpec{
		{Kind: "validate", Model: "glm-4.1v", Engine: "sglang"},
		{Kind: "validate", Model: "qwen3-8b", Engine: "vllm"},
	}

	filtered := planner.filterTaskSpecs(input, tasks)
	if len(filtered) != 2 {
		t.Fatalf("filtered len=%d, want 2", len(filtered))
	}
	if filtered[0].Model != "qwen3-8b" || filtered[0].Engine != "vllm" {
		t.Fatalf("first task=%+v, want pending-work combo first", filtered[0])
	}
}

func TestExplorerBuildPlanInput_DerivesModelScopeSkipFromExhaustedReadyCombos(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	e := NewExplorer(ExplorerConfig{
		Schedule: DefaultScheduleConfig(),
		Enabled:  true,
	}, nil, nil, db, NewEventBus(),
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "test"}, nil
		}),
		WithGatherLocalModels(func(ctx context.Context) ([]LocalModel, error) {
			return []LocalModel{
				{Name: "qwen3-8b", Type: "llm"},
				{Name: "glm-4.1v", Type: "llm"},
			}, nil
		}),
		WithGatherLocalEngines(func(ctx context.Context) ([]LocalEngine, error) {
			return []LocalEngine{
				{Name: "vllm", Type: "vllm"},
				{Name: "sglang", Type: "sglang"},
			}, nil
		}),
		WithGatherComboFacts(func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error) {
			return []ComboFact{
				{Model: "qwen3-8b", Engine: "vllm", Status: "ready"},
				{Model: "qwen3-8b", Engine: "sglang", Status: "ready"},
				{Model: "glm-4.1v", Engine: "sglang", Status: "ready"},
			}, nil
		}),
	)
	for _, engine := range []string{"vllm", "sglang"} {
		run := &state.ExplorationRun{
			ID:         "run-" + engine,
			Kind:       "validate",
			HardwareID: "test-hw",
			ModelID:    "qwen3-8b",
			EngineID:   engine,
			Status:     "failed",
			Error:      "pre-flight deploy: wait for deployed endpoint qwen3-8b: timeout waiting for inference endpoint",
		}
		if err := db.InsertExplorationRun(context.Background(), run); err != nil {
			t.Fatalf("InsertExplorationRun(%s): %v", engine, err)
		}
	}

	input, err := e.buildPlanInput(context.Background(), nil)
	if err != nil {
		t.Fatalf("buildPlanInput: %v", err)
	}
	found := false
	for _, skip := range input.SkipCombos {
		if skip.Model == "qwen3-8b" && skip.Engine == "" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SkipCombos = %+v, want model-scope blocker entry", input.SkipCombos)
	}
}

func TestExplorerBuildPlanInput_KeepsCompletedComboWithPendingCoverageAndTune(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	run := &state.ExplorationRun{
		ID:          "run-completed",
		Kind:        "validate",
		HardwareID:  "test-hw",
		ModelID:     "qwen3-8b",
		EngineID:    "vllm-nightly",
		Status:      "completed",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
		StartedAt:   time.Now(),
		CompletedAt: time.Now(),
	}
	if err := db.InsertExplorationRun(ctx, run); err != nil {
		t.Fatalf("InsertExplorationRun: %v", err)
	}
	cfg := &state.Configuration{
		ID:         "cfg-qwen3-8b-nightly",
		HardwareID: "test-hw",
		EngineID:   "vllm-nightly",
		ModelID:    "qwen3-8b",
		Config:     `{"gpu_memory_utilization":0.85,"max_model_len":8192}`,
		ConfigHash: "cfg-qwen3-8b-nightly",
		Status:     "experiment",
		Source:     "test",
	}
	if err := db.InsertConfiguration(ctx, cfg); err != nil {
		t.Fatalf("InsertConfiguration: %v", err)
	}
	bench := &state.BenchmarkResult{
		ID:             "bench-qwen3-8b-nightly",
		ConfigID:       cfg.ID,
		Concurrency:    1,
		InputLenBucket: "8192-16383",
		Modality:       "text",
		ThroughputTPS:  42.0,
		TTFTP50ms:      25,
		TPOTP50ms:      0.2,
	}
	if err := db.InsertBenchmarkResult(ctx, bench); err != nil {
		t.Fatalf("InsertBenchmarkResult: %v", err)
	}

	e := NewExplorer(ExplorerConfig{Schedule: DefaultScheduleConfig()}, nil, nil, db, NewEventBus(),
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "blackwell", GPUCount: 1, VRAMMiB: 120000}, nil
		}),
		WithGatherLocalModels(func(ctx context.Context) ([]LocalModel, error) {
			return []LocalModel{
				{Name: "qwen3-8b", Type: "llm", MaxContextLen: 32768},
			}, nil
		}),
		WithGatherLocalEngines(func(ctx context.Context) ([]LocalEngine, error) {
			return []LocalEngine{
				{
					Name: "vllm-nightly",
					Type: "vllm-nightly",
					TunableParams: map[string]any{
						"gpu_memory_utilization": 0.85,
						"max_model_len":          8192,
					},
				},
			}, nil
		}),
		WithGatherComboFacts(func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error) {
			return []ComboFact{
				{Model: "qwen3-8b", Engine: "vllm-nightly", Status: "ready"},
			}, nil
		}),
	)

	input, err := e.buildPlanInput(ctx, nil)
	if err != nil {
		t.Fatalf("buildPlanInput: %v", err)
	}
	if hasPendingWorkFor(input.PendingWork, "qwen3-8b", "vllm-nightly") != true {
		t.Fatalf("PendingWork = %+v, want qwen3-8b/vllm-nightly obligations", input.PendingWork)
	}
	var kinds []string
	for _, work := range input.PendingWork {
		if work.Model == "qwen3-8b" && work.Engine == "vllm-nightly" {
			kinds = append(kinds, work.Kind)
		}
	}
	sort.Strings(kinds)
	if !reflect.DeepEqual(kinds, []string{"tune", "validate_long_context"}) {
		t.Fatalf("pending work kinds = %v, want [tune validate_long_context]", kinds)
	}
	for _, skip := range input.SkipCombos {
		if skip.Model == "qwen3-8b" && skip.Engine == "vllm-nightly" {
			t.Fatalf("completed combo with pending work should stay executable, skip=%+v", skip)
		}
	}
}

func TestExplorerBuildPlanInput_DerivesFamilyArtifactSkipFromRepeatedFailures(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(filepath.Join(dir, "workspace"))
	if err := ws.Init(); err != nil {
		t.Fatalf("workspace.Init: %v", err)
	}

	stableArtifact := "qujing/vllm-gemma4-gb10:0.19.0-torchmoe2"
	failed := ExperimentResult{
		Status: "failed",
		Error:  "pre-flight deploy: wait for deployed endpoint glm: timeout waiting for inference endpoint glm (10m0s): ModuleNotFoundError: No module named 'compressed_tensors'",
	}
	for i, task := range []TaskSpec{
		{Kind: "validate", Model: "GLM-4.6V-Flash-FP4", Engine: "vllm", Reason: "seed"},
		{Kind: "validate", Model: "GLM-4.1V-9B-Thinking", Engine: "vllm", Reason: "seed"},
	} {
		if _, err := ws.WriteExperimentResult(i+1, task, failed); err != nil {
			t.Fatalf("WriteExperimentResult(%d): %v", i+1, err)
		}
	}

	e := NewExplorer(ExplorerConfig{Schedule: DefaultScheduleConfig()}, nil, nil, nil, NewEventBus(),
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "blackwell"}, nil
		}),
		WithGatherLocalModels(func(ctx context.Context) ([]LocalModel, error) {
			return []LocalModel{
				{Name: "GLM-4.6V-Flash-FP4", Type: "llm", Family: "glm"},
				{Name: "GLM-4.1V-9B-Thinking", Type: "llm", Family: "glm"},
				{Name: "GLM-4.1V-9B-Thinking-FP4", Type: "llm", Family: "glm"},
				{Name: "GLM-4.7-Flash-NVFP4", Type: "llm", Family: "glm"},
				{Name: "Qwen2.5-Coder-3B-Instruct", Type: "llm", Family: "qwen"},
			}, nil
		}),
		WithGatherLocalEngines(func(ctx context.Context) ([]LocalEngine, error) {
			return []LocalEngine{
				{Name: "vllm", Type: "vllm", Artifact: stableArtifact},
			}, nil
		}),
		WithGatherComboFacts(func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error) {
			return []ComboFact{
				{Model: "GLM-4.6V-Flash-FP4", Engine: "vllm", Artifact: stableArtifact, Status: "blocked", Reason: "historical structural failure"},
				{Model: "GLM-4.1V-9B-Thinking", Engine: "vllm", Artifact: stableArtifact, Status: "blocked", Reason: "historical structural failure"},
				{Model: "GLM-4.1V-9B-Thinking-FP4", Engine: "vllm", Artifact: stableArtifact, Status: "ready"},
				{Model: "GLM-4.7-Flash-NVFP4", Engine: "vllm", Artifact: stableArtifact, Status: "ready"},
				{Model: "Qwen2.5-Coder-3B-Instruct", Engine: "vllm", Artifact: stableArtifact, Status: "ready"},
			}, nil
		}),
	)
	e.workspace = ws

	input, err := e.buildPlanInput(context.Background(), nil)
	if err != nil {
		t.Fatalf("buildPlanInput: %v", err)
	}

	got := map[string]string{}
	for _, skip := range input.SkipCombos {
		got[skip.Model+"|"+skip.Engine] = skip.Reason
	}
	for _, combo := range []string{
		"GLM-4.1V-9B-Thinking-FP4|vllm",
		"GLM-4.7-Flash-NVFP4|vllm",
	} {
		reason := got[combo]
		if !strings.Contains(reason, "repeated structural failure") || !strings.Contains(reason, "compressed_tensors") {
			t.Fatalf("%s reason = %q, want repeated family blocker mentioning compressed_tensors", combo, reason)
		}
	}
	if reason := got["Qwen2.5-Coder-3B-Instruct|vllm"]; reason != "" {
		t.Fatalf("qwen combo unexpectedly skipped: %q", reason)
	}
}

func TestExplorerBuildPlanInput_DerivesFamilyArtifactSkipFromHistoricalRuns(t *testing.T) {
	dir := t.TempDir()
	db, err := state.Open(context.Background(), filepath.Join(dir, "explorer.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	defer db.Close()

	now := time.Now()
	runs := []*state.ExplorationRun{
		{
			ID:          "run-glm-a",
			Kind:        "validate",
			Status:      "failed",
			HardwareID:  "test-hw",
			ModelID:     "GLM-4.6V-Flash-FP4",
			EngineID:    "vllm",
			Error:       "pre-flight deploy: wait for deployed endpoint glm: timeout waiting for inference endpoint glm (10m0s): ModuleNotFoundError: No module named 'compressed_tensors'",
			CreatedAt:   now,
			UpdatedAt:   now,
			StartedAt:   now,
			CompletedAt: now,
		},
		{
			ID:          "run-glm-b",
			Kind:        "validate",
			Status:      "failed",
			HardwareID:  "test-hw",
			ModelID:     "GLM-4.1V-9B-Thinking",
			EngineID:    "vllm",
			Error:       "pre-flight deploy: wait for deployed endpoint glm: timeout waiting for inference endpoint glm (10m0s): ModuleNotFoundError: No module named 'compressed_tensors'",
			CreatedAt:   now.Add(time.Second),
			UpdatedAt:   now.Add(time.Second),
			StartedAt:   now.Add(time.Second),
			CompletedAt: now.Add(time.Second),
		},
	}
	for i, run := range runs {
		if err := db.InsertExplorationRun(context.Background(), run); err != nil {
			t.Fatalf("InsertExplorationRun(%d): %v", i, err)
		}
	}

	e := NewExplorer(ExplorerConfig{Schedule: DefaultScheduleConfig()}, nil, nil, db, NewEventBus(),
		WithGatherHardware(func(ctx context.Context) (HardwareInfo, error) {
			return HardwareInfo{Profile: "test-hw", GPUArch: "blackwell"}, nil
		}),
		WithGatherLocalModels(func(ctx context.Context) ([]LocalModel, error) {
			return []LocalModel{
				{Name: "GLM-4.6V-Flash-FP4", Type: "llm", Family: "glm"},
				{Name: "GLM-4.1V-9B-Thinking", Type: "llm", Family: "glm"},
				{Name: "GLM-4.5-Air-nvfp4", Type: "llm", Family: "glm"},
				{Name: "Qwen2.5-Coder-3B-Instruct", Type: "llm", Family: "qwen"},
			}, nil
		}),
		WithGatherLocalEngines(func(ctx context.Context) ([]LocalEngine, error) {
			return []LocalEngine{
				{Name: "vllm", Type: "vllm", Artifact: "qujing/vllm-gemma4-gb10:0.19.0-torchmoe2"},
			}, nil
		}),
		WithGatherComboFacts(func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error) {
			return []ComboFact{
				{Model: "GLM-4.6V-Flash-FP4", Engine: "vllm", Artifact: "qujing/vllm-gemma4-gb10:0.19.0-torchmoe2", Status: "blocked", Reason: "historical structural failure"},
				{Model: "GLM-4.1V-9B-Thinking", Engine: "vllm", Artifact: "qujing/vllm-gemma4-gb10:0.19.0-torchmoe2", Status: "blocked", Reason: "historical structural failure"},
				{Model: "GLM-4.5-Air-nvfp4", Engine: "vllm", Artifact: "qujing/vllm-gemma4-gb10:0.19.0-torchmoe2", Status: "ready"},
				{Model: "Qwen2.5-Coder-3B-Instruct", Engine: "vllm", Artifact: "qujing/vllm-gemma4-gb10:0.19.0-torchmoe2", Status: "ready"},
			}, nil
		}),
	)

	input, err := e.buildPlanInput(context.Background(), nil)
	if err != nil {
		t.Fatalf("buildPlanInput: %v", err)
	}

	got := map[string]string{}
	for _, skip := range input.SkipCombos {
		got[skip.Model+"|"+skip.Engine] = skip.Reason
	}
	if reason := got["GLM-4.5-Air-nvfp4|vllm"]; !strings.Contains(reason, "compressed_tensors") {
		t.Fatalf("historical family blocker not derived for GLM-4.5-Air-nvfp4|vllm: %q", reason)
	}
	if reason := got["Qwen2.5-Coder-3B-Instruct|vllm"]; reason != "" {
		t.Fatalf("qwen combo unexpectedly skipped from historical glm blocker: %q", reason)
	}
}

func TestExplorer_HandleEventClosesPlanDocumentAfterRun(t *testing.T) {
	dir := t.TempDir()
	agent := NewAgent(&mockLLM{}, &mockTools{})
	agent.mode = toolModeContextOnly
	e := NewExplorer(ExplorerConfig{
		Schedule:  DefaultScheduleConfig(),
		Enabled:   true,
		Mode:      "budget",
		MaxRounds: 1,
	}, agent, nil, nil, NewEventBus())
	e.planner = &countingPlanner{executed: new(atomic.Int32)}
	e.workspace = NewExplorerWorkspace(filepath.Join(dir, "workspace"))

	e.handleEvent(context.Background(), ExplorerEvent{Type: EventScheduledGapScan})

	plan, err := e.workspace.ReadFile("plan.md")
	if err != nil {
		t.Fatalf("ReadFile(plan.md): %v", err)
	}
	for _, want := range []string{
		"No pending executable plan",
		"Explorer state: `budget_exhausted`",
		"No pending executable tasks",
		"```yaml\n[]\n```",
	} {
		if !strings.Contains(plan, want) {
			t.Fatalf("closed plan missing %q:\n%s", want, plan)
		}
	}
}

func TestBenchmarkEntriesFromMatrixJSON_PreservesLatencyP50(t *testing.T) {
	matrixJSON := `{
		"matrix_profiles": [{
			"label": "plan",
			"cells": [{
				"concurrency": 1,
				"input_tokens": 128,
				"max_tokens": 256,
				"result": {
					"throughput_tps": 120.0,
					"ttft_p50_ms": 10.0,
					"ttft_p95_ms": 35.0,
					"tpot_p50_ms": 0.10,
					"tpot_p95_ms": 0.25
				}
			}]
		}]
	}`

	entries := benchmarkEntriesFromMatrixJSON(matrixJSON)
	if len(entries) != 1 {
		t.Fatalf("entries len=%d, want 1", len(entries))
	}
	if entries[0].TTFTP50Ms != 10 {
		t.Fatalf("TTFTP50Ms=%v, want 10", entries[0].TTFTP50Ms)
	}
	if entries[0].TPOTP50Ms != 0.10 {
		t.Fatalf("TPOTP50Ms=%v, want 0.10", entries[0].TPOTP50Ms)
	}
	if entries[0].LatencyP50Ms <= 35 || entries[0].LatencyP50Ms >= 36 {
		t.Fatalf("LatencyP50Ms=%v, want derived value near 35.6", entries[0].LatencyP50Ms)
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
		{
			"skips_no_output_cells",
			`{"matrix_profiles":[{"label":"latency","cells":[
					{"concurrency":1,"input_tokens":1024,"max_tokens":1024,"result":{"throughput_tps":0,"successful_requests":0,"avg_output_tokens":0}},
					{"concurrency":1,"input_tokens":512,"max_tokens":512,"result":{"throughput_tps":88,"successful_requests":2,"avg_output_tokens":256}}
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
