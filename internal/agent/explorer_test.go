package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
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
