# Edge Explorer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Explorer subsystem that autonomously discovers knowledge on edge devices via scheduled gap scanning, event-driven triggers, and Tier 1/2 planning.

**Architecture:** Explorer orchestrates Scheduler (timer loops) + EventBus (event pub/sub) + Planner (rule-based or LLM-driven) + Harvester (result collection). It wraps existing ExplorationManager/Tuner/Benchmark as executors. Patrol emits events to EventBus.

**Tech Stack:** Go 1.22+, zero CGO, log/slog, sync primitives, existing agent/state/mcp packages.

**Design Spec:** `docs/superpowers/specs/2026-04-07-v0.4-knowledge-automation-design.md`

---

## File Structure

```
internal/agent/
  explorer_eventbus.go      # ExplorerEvent type, EventBus (chan-based pub/sub)
  explorer_eventbus_test.go  # EventBus unit tests
  explorer_planner.go        # Planner interface, PlanInput, ExplorationPlan, PlanTask, RulePlanner
  explorer_planner_test.go   # RulePlanner unit tests
  explorer_llmplanner.go     # LLMPlanner (Tier 2)
  explorer_llmplanner_test.go
  explorer_harvester.go      # Harvester (template + LLM modes)
  explorer_harvester_test.go
  explorer_scheduler.go      # ScheduleConfig, Scheduler (timer loops + quiet hours)
  explorer_scheduler_test.go
  explorer.go                # Explorer (orchestrator: tier detection, lifecycle, main loop)
  explorer_test.go
  patrol.go                  # MODIFY: add EventBus emission on alerts

internal/state/
  sqlite.go                  # MODIFY: add migrateV12 for exploration_plans table
  types.go                   # MODIFY: add ExplorationPlanRow type

cmd/aima/
  explorer.go                # New CLI: aima explorer status/trigger/config
  main.go                    # MODIFY: wire Explorer into appContext + buildToolDeps

internal/mcp/
  tools_explorer.go          # New: explorer.status, explorer.config, explorer.trigger
  tools_deps.go              # MODIFY: add Explorer-related fields to ToolDeps
```

---

### Task 1: EventBus

**Files:**
- Create: `internal/agent/explorer_eventbus.go`
- Create: `internal/agent/explorer_eventbus_test.go`

- [ ] **Step 1: Write EventBus types and test**

```go
// explorer_eventbus_test.go
package agent

import (
	"testing"
	"time"
)

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()

	bus.Publish(ExplorerEvent{Type: EventDeployCompleted, Model: "qwen3-8b"})

	select {
	case ev := <-ch:
		if ev.Type != EventDeployCompleted {
			t.Errorf("type = %q, want %q", ev.Type, EventDeployCompleted)
		}
		if ev.Model != "qwen3-8b" {
			t.Errorf("model = %q, want %q", ev.Model, "qwen3-8b")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	ch1 := bus.Subscribe()
	ch2 := bus.Subscribe()

	bus.Publish(ExplorerEvent{Type: EventPatrolOOM})

	for _, ch := range []<-chan ExplorerEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != EventPatrolOOM {
				t.Errorf("type = %q, want %q", ev.Type, EventPatrolOOM)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatal("timeout")
		}
	}
}

func TestEventBus_NonBlocking(t *testing.T) {
	bus := NewEventBus()
	_ = bus.Subscribe() // subscriber that never reads

	// Publish should not block even if subscriber isn't reading
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			bus.Publish(ExplorerEvent{Type: EventPatrolIdle})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked")
	}
}

func TestEventBus_Unsubscribe(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	bus.Unsubscribe(ch)

	bus.Publish(ExplorerEvent{Type: EventPatrolOOM})

	select {
	case <-ch:
		t.Fatal("received event after unsubscribe")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestEventBus -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement EventBus**

```go
// explorer_eventbus.go
package agent

import (
	"sync"
	"time"
)

// Event types emitted by various AIMA components.
const (
	EventDeployCompleted = "deploy.completed"
	EventPatrolOOM       = "patrol.alert.oom"
	EventPatrolIdle      = "patrol.alert.idle"
	EventModelDiscovered = "model.discovered"
	EventCentralAdvisory = "central.advisory"
	EventCentralScenario = "central.scenario"
	EventScheduledGapScan = "scheduled.gap_scan"
	EventScheduledAudit   = "scheduled.full_audit"
	EventScheduledSync    = "scheduled.sync"
)

// ExplorerEvent carries event data through the EventBus.
type ExplorerEvent struct {
	Type      string
	Model     string
	Engine    string
	AlertID   string
	Advisory  json.RawMessage // advisory payload for central.advisory events
	Timestamp time.Time
}

// EventBus is a simple in-process pub/sub for Explorer events.
type EventBus struct {
	mu   sync.RWMutex
	subs map[chan ExplorerEvent]struct{}
}

func NewEventBus() *EventBus {
	return &EventBus{subs: make(map[chan ExplorerEvent]struct{})}
}

func (b *EventBus) Subscribe() <-chan ExplorerEvent {
	ch := make(chan ExplorerEvent, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *EventBus) Unsubscribe(ch <-chan ExplorerEvent) {
	// Type assertion to get the writable channel for map lookup.
	wch := ch.(chan ExplorerEvent)
	b.mu.Lock()
	delete(b.subs, wch)
	b.mu.Unlock()
}

func (b *EventBus) Publish(ev ExplorerEvent) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default:
			// Drop if subscriber is full — non-blocking.
		}
	}
}
```

Note: The `Unsubscribe` method needs the chan to be castable. Since `Subscribe()` returns `<-chan`, the test calls `Unsubscribe` with the same value. Adjust the interface: store the writable `chan ExplorerEvent` internally, return `<-chan` to callers. For `Unsubscribe`, accept `<-chan` and iterate to find the matching channel pointer. A simpler approach: return a writable chan and let the caller use it as receive-only.

Revised: change `Subscribe()` to return `chan ExplorerEvent` and let callers treat it as receive-only. Update `Unsubscribe` signature to `Unsubscribe(ch chan ExplorerEvent)`. Update test accordingly.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestEventBus -v`
Expected: PASS (all 4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_eventbus.go internal/agent/explorer_eventbus_test.go
git commit -m "feat(explorer): add EventBus for in-process event pub/sub"
```

---

### Task 2: Planner Interface + RulePlanner (Tier 1)

**Files:**
- Create: `internal/agent/explorer_planner.go`
- Create: `internal/agent/explorer_planner_test.go`

- [ ] **Step 1: Write Planner types and RulePlanner test**

```go
// explorer_planner_test.go
package agent

import (
	"context"
	"testing"

	"github.com/anthropics/aima/internal/state"
)

func TestRulePlanner_DeployedWithoutBenchmark(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{
		ActiveDeploys: []DeployStatus{
			{Model: "qwen3-8b", Engine: "vllm", Status: "running"},
		},
		Gaps:         nil,
		Advisories:   nil,
		History:      nil,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) == 0 {
		t.Fatal("expected at least 1 task for deployed model without benchmark")
	}
	if plan.Tasks[0].Kind != "validate" {
		t.Errorf("first task kind = %q, want validate", plan.Tasks[0].Kind)
	}
	if plan.Tasks[0].Model != "qwen3-8b" {
		t.Errorf("first task model = %q, want qwen3-8b", plan.Tasks[0].Model)
	}
	if plan.Tier != 1 {
		t.Errorf("tier = %d, want 1", plan.Tier)
	}
}

func TestRulePlanner_AdvisoryPriority(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{
		Advisories: []Advisory{
			{ID: "adv-1", TargetModel: "qwen3-30b", TargetEngine: "vllm", Config: map[string]any{"gpu_memory_utilization": 0.78}},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) == 0 {
		t.Fatal("expected task for advisory")
	}
	found := false
	for _, task := range plan.Tasks {
		if task.Model == "qwen3-30b" && task.Kind == "validate" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no validate task for advisory model qwen3-30b")
	}
}

func TestRulePlanner_GapsLimited(t *testing.T) {
	p := &RulePlanner{}
	gaps := make([]GapEntry, 10)
	for i := range gaps {
		gaps[i] = GapEntry{Model: fmt.Sprintf("model-%d", i), Engine: "vllm"}
	}
	plan, err := p.Plan(context.Background(), PlanInput{Gaps: gaps})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Should limit to 3 gap tasks per cycle
	gapTasks := 0
	for _, task := range plan.Tasks {
		if task.Reason == "knowledge gap" {
			gapTasks++
		}
	}
	if gapTasks > 3 {
		t.Errorf("gap tasks = %d, want <= 3", gapTasks)
	}
}

func TestRulePlanner_EmptyInput(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) != 0 {
		t.Errorf("tasks = %d, want 0 for empty input", len(plan.Tasks))
	}
}

func TestRulePlanner_OpenQuestions(t *testing.T) {
	p := &RulePlanner{}
	plan, err := p.Plan(context.Background(), PlanInput{
		OpenQuestions: []OpenQuestion{
			{ID: "q-1", Status: "untested", Model: "qwen3-8b"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	found := false
	for _, task := range plan.Tasks {
		if task.Kind == "open_question" {
			found = true
			break
		}
	}
	if !found {
		t.Error("no open_question task for untested question")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestRulePlanner -v`
Expected: FAIL — types not defined

- [ ] **Step 3: Implement Planner types and RulePlanner**

```go
// explorer_planner.go
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Planner generates exploration plans from device state.
type Planner interface {
	Plan(ctx context.Context, input PlanInput) (*ExplorationPlan, error)
}

// PlanInput aggregates all context needed for plan generation.
type PlanInput struct {
	Hardware      HardwareInfo
	Gaps          []GapEntry
	ActiveDeploys []DeployStatus
	Advisories    []Advisory
	History       []ExplorationRun
	OpenQuestions []OpenQuestion
	Event         *ExplorerEvent
}

type HardwareInfo struct {
	Profile  string
	GPUArch  string
	GPUCount int
	VRAMMiB  int
}

type DeployStatus struct {
	Model  string
	Engine string
	Status string
}

type GapEntry struct {
	Model          string
	Engine         string
	Hardware       string
	BenchmarkCount int
}

type Advisory struct {
	ID           string
	Type         string
	TargetModel  string
	TargetEngine string
	Config       map[string]any
	Confidence   string
	Reasoning    string
}

type OpenQuestion struct {
	ID       string
	Model    string
	Engine   string
	Question string
	Status   string
}

// ExplorationPlan is an ordered list of exploration tasks.
type ExplorationPlan struct {
	ID        string
	Tier      int
	Tasks     []PlanTask
	Reasoning string
}

// PlanTask is a single exploration unit.
type PlanTask struct {
	Kind      string         // "validate", "tune", "open_question", "compare"
	Model     string
	Engine    string
	Params    map[string]any
	Reason    string
	Priority  int
	DependsOn string
}

// RulePlanner generates plans using fixed priority rules (Tier 1).
type RulePlanner struct{}

func (p *RulePlanner) Plan(ctx context.Context, input PlanInput) (*ExplorationPlan, error) {
	var tasks []PlanTask

	// Rule 1: deployed models without benchmarks — highest priority
	for _, d := range input.ActiveDeploys {
		if d.Status != "running" {
			continue
		}
		if !hasHistoryFor(input.History, d.Model, d.Engine) {
			tasks = append(tasks, PlanTask{
				Kind:     "validate",
				Model:    d.Model,
				Engine:   d.Engine,
				Priority: 0,
				Reason:   "deployed without benchmark baseline",
			})
		}
	}

	// Rule 2: central advisories — verify recommended configs
	for _, adv := range input.Advisories {
		tasks = append(tasks, PlanTask{
			Kind:     "validate",
			Model:    adv.TargetModel,
			Engine:   adv.TargetEngine,
			Params:   adv.Config,
			Priority: 1,
			Reason:   fmt.Sprintf("verify central advisory %s", adv.ID),
		})
	}

	// Rule 3: knowledge gaps — max 3 per cycle, sorted by model name (stable)
	sorted := make([]GapEntry, len(input.Gaps))
	copy(sorted, input.Gaps)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Model < sorted[j].Model })
	for i, gap := range sorted {
		if i >= 3 {
			break
		}
		tasks = append(tasks, PlanTask{
			Kind:     "validate",
			Model:    gap.Model,
			Engine:   gap.Engine,
			Priority: 2 + i,
			Reason:   "knowledge gap",
		})
	}

	// Rule 4: untested open questions
	for _, q := range input.OpenQuestions {
		if q.Status != "untested" {
			continue
		}
		tasks = append(tasks, PlanTask{
			Kind:     "open_question",
			Model:    q.Model,
			Engine:   q.Engine,
			Priority: 5,
			Reason:   fmt.Sprintf("open question %s", q.ID),
		})
	}

	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Priority < tasks[j].Priority })

	id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), len(tasks)))))[:8]
	return &ExplorationPlan{
		ID:        id,
		Tier:      1,
		Tasks:     tasks,
		Reasoning: "rule-based",
	}, nil
}

func hasHistoryFor(history []ExplorationRun, model, engine string) bool {
	for _, h := range history {
		if h.ModelID == model && h.EngineID == engine && h.Status == "completed" {
			return true
		}
	}
	return false
}

// ExplorationRun is re-exported from state for plan input convenience.
// Use state.ExplorationRun directly where imported.
type ExplorationRun = state.ExplorationRun
```

Note: `ExplorationRun` is an alias to `state.ExplorationRun` to avoid import conflicts in PlanInput. If the `state` package import is not available in the `agent` package, define the alias or use the state type directly. Check existing imports in `agent/exploration.go` — it already imports `state`, so this should work.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestRulePlanner -v`
Expected: PASS (all 5 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_planner.go internal/agent/explorer_planner_test.go
git commit -m "feat(explorer): add Planner interface and RulePlanner (Tier 1)"
```

---

### Task 3: Scheduler

**Files:**
- Create: `internal/agent/explorer_scheduler.go`
- Create: `internal/agent/explorer_scheduler_test.go`

- [ ] **Step 1: Write Scheduler test**

```go
// explorer_scheduler_test.go
package agent

import (
	"testing"
	"time"
)

func TestScheduleConfig_Defaults(t *testing.T) {
	c := DefaultScheduleConfig()
	if c.GapScanInterval != 24*time.Hour {
		t.Errorf("GapScanInterval = %v, want 24h", c.GapScanInterval)
	}
	if c.MaxConcurrentRuns != 1 {
		t.Errorf("MaxConcurrentRuns = %d, want 1", c.MaxConcurrentRuns)
	}
}

func TestScheduler_IsQuietHour(t *testing.T) {
	s := &Scheduler{config: ScheduleConfig{QuietStart: 2, QuietEnd: 6}}
	tests := []struct {
		hour int
		want bool
	}{
		{1, false},
		{2, true},
		{4, true},
		{5, true},
		{6, false},
		{12, false},
		{23, false},
	}
	for _, tt := range tests {
		got := s.isQuietHour(tt.hour)
		if got != tt.want {
			t.Errorf("isQuietHour(%d) = %v, want %v", tt.hour, got, tt.want)
		}
	}
}

func TestScheduler_EmitsEvents(t *testing.T) {
	bus := NewEventBus()
	ch := bus.Subscribe()
	s := NewScheduler(ScheduleConfig{
		GapScanInterval: 50 * time.Millisecond,
		SyncInterval:    0, // disabled
		FullAuditInterval: 0, // disabled
		MaxConcurrentRuns: 1,
		QuietStart: 0,
		QuietEnd:   0, // no quiet hours
	}, bus)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	go s.Start(ctx)

	// Should receive at least one gap scan event
	select {
	case ev := <-ch:
		if ev.Type != EventScheduledGapScan {
			t.Errorf("type = %q, want %q", ev.Type, EventScheduledGapScan)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for scheduled event")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestSchedul -v`

- [ ] **Step 3: Implement Scheduler**

```go
// explorer_scheduler.go
package agent

import (
	"context"
	"log/slog"
	"time"
)

// ScheduleConfig controls the Explorer's periodic behavior.
type ScheduleConfig struct {
	GapScanInterval   time.Duration // default 24h
	FullAuditInterval time.Duration // default 7d
	SyncInterval      time.Duration // default 6h
	MaxConcurrentRuns int           // default 1
	QuietStart        int           // hour 0-23, default 2
	QuietEnd          int           // hour 0-23, default 6
}

func DefaultScheduleConfig() ScheduleConfig {
	return ScheduleConfig{
		GapScanInterval:   24 * time.Hour,
		FullAuditInterval: 7 * 24 * time.Hour,
		SyncInterval:      6 * time.Hour,
		MaxConcurrentRuns: 1,
		QuietStart:        2,
		QuietEnd:          6,
	}
}

// Scheduler emits timed ExplorerEvents to the EventBus.
type Scheduler struct {
	config ScheduleConfig
	bus    *EventBus
}

func NewScheduler(config ScheduleConfig, bus *EventBus) *Scheduler {
	return &Scheduler{config: config, bus: bus}
}

// Start runs timer loops until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.runLoop(ctx, s.config.GapScanInterval, EventScheduledGapScan)
	// Additional loops for sync and audit run in parallel goroutines
}

// StartAll starts all timer loops concurrently.
func (s *Scheduler) StartAll(ctx context.Context) {
	if s.config.GapScanInterval > 0 {
		go s.runLoop(ctx, s.config.GapScanInterval, EventScheduledGapScan)
	}
	if s.config.SyncInterval > 0 {
		go s.runLoop(ctx, s.config.SyncInterval, EventScheduledSync)
	}
	if s.config.FullAuditInterval > 0 {
		go s.runLoop(ctx, s.config.FullAuditInterval, EventScheduledAudit)
	}
}

func (s *Scheduler) runLoop(ctx context.Context, interval time.Duration, eventType string) {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if s.isQuietHour(time.Now().Hour()) {
				slog.Debug("scheduler: quiet hour, skipping", "event", eventType)
			} else {
				s.bus.Publish(ExplorerEvent{Type: eventType})
			}
			timer.Reset(interval)
		}
	}
}

func (s *Scheduler) isQuietHour(hour int) bool {
	if s.config.QuietStart == s.config.QuietEnd {
		return false // no quiet hours configured
	}
	return hour >= s.config.QuietStart && hour < s.config.QuietEnd
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestSchedul -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_scheduler.go internal/agent/explorer_scheduler_test.go
git commit -m "feat(explorer): add Scheduler with timer loops and quiet hours"
```

---

### Task 4: Harvester (Tier 1 Template Mode)

**Files:**
- Create: `internal/agent/explorer_harvester.go`
- Create: `internal/agent/explorer_harvester_test.go`

- [ ] **Step 1: Write Harvester test**

```go
// explorer_harvester_test.go
package agent

import (
	"context"
	"testing"
)

func TestHarvester_TemplateNote(t *testing.T) {
	h := &Harvester{tier: 1}
	note := h.generateNote(HarvestInput{
		Task: PlanTask{Model: "qwen3-8b", Engine: "vllm"},
		Result: HarvestResult{
			Success:    true,
			Throughput: 45.2,
			TTFTP95:    280.0,
			Config:     map[string]any{"gpu_memory_utilization": 0.85},
		},
	})
	if note == "" {
		t.Fatal("expected non-empty note")
	}
	// Template note should contain model and throughput
	if !strings.Contains(note, "qwen3-8b") {
		t.Error("note missing model name")
	}
	if !strings.Contains(note, "45.2") {
		t.Error("note missing throughput")
	}
}

func TestHarvester_ShouldPromote(t *testing.T) {
	h := &Harvester{tier: 1}
	tests := []struct {
		name       string
		result     HarvestResult
		wantPromote bool
	}{
		{"success", HarvestResult{Success: true, Promoted: true}, true},
		{"failed", HarvestResult{Success: false}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions := h.Harvest(context.Background(), HarvestInput{
				Task:   PlanTask{Model: "qwen3-8b", Engine: "vllm"},
				Result: tt.result,
			})
			hasPromote := false
			for _, a := range actions {
				if a.Type == "promote" {
					hasPromote = true
				}
			}
			if hasPromote != tt.wantPromote {
				t.Errorf("promote = %v, want %v", hasPromote, tt.wantPromote)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

- [ ] **Step 3: Implement Harvester**

```go
// explorer_harvester.go
package agent

import (
	"context"
	"fmt"
	"log/slog"
)

// HarvestInput contains the exploration task result for post-processing.
type HarvestInput struct {
	Task   PlanTask
	Result HarvestResult
}

// HarvestResult captures benchmark/exploration outcomes.
type HarvestResult struct {
	Success    bool
	Throughput float64
	TTFTP95    float64
	VRAMMiB    float64
	Config     map[string]any
	Promoted   bool   // set by maybeAutoPromote
	Error      string
}

// HarvestAction describes a post-exploration side effect.
type HarvestAction struct {
	Type   string // "promote", "note", "sync_push", "update_question", "feedback"
	Detail string
}

// Harvester collects exploration results and performs post-processing.
type Harvester struct {
	tier     int
	llm      LLMClient // nil for Tier 1
	syncPush func(ctx context.Context) error
	saveNote func(ctx context.Context, title, content, hardware, model, engine string) error
}

type HarvesterOption func(*Harvester)

func WithHarvesterLLM(llm LLMClient) HarvesterOption {
	return func(h *Harvester) { h.llm = llm }
}

func WithSyncPush(fn func(ctx context.Context) error) HarvesterOption {
	return func(h *Harvester) { h.syncPush = fn }
}

func WithSaveNote(fn func(ctx context.Context, title, content, hardware, model, engine string) error) HarvesterOption {
	return func(h *Harvester) { h.saveNote = fn }
}

func NewHarvester(tier int, opts ...HarvesterOption) *Harvester {
	h := &Harvester{tier: tier}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Harvest processes an exploration result and returns actions taken.
func (h *Harvester) Harvest(ctx context.Context, input HarvestInput) []HarvestAction {
	var actions []HarvestAction

	if !input.Result.Success {
		note := fmt.Sprintf("%s on %s: FAILED — %s", input.Task.Model, input.Task.Engine, input.Result.Error)
		actions = append(actions, HarvestAction{Type: "note", Detail: note})
		if h.saveNote != nil {
			_ = h.saveNote(ctx, "exploration failed", note, "", input.Task.Model, input.Task.Engine)
		}
		return actions
	}

	// Record knowledge note
	note := h.generateNote(input)
	actions = append(actions, HarvestAction{Type: "note", Detail: note})
	if h.saveNote != nil {
		title := fmt.Sprintf("%s on %s benchmark", input.Task.Model, input.Task.Engine)
		_ = h.saveNote(ctx, title, note, "", input.Task.Model, input.Task.Engine)
	}

	// Track promotion
	if input.Result.Promoted {
		actions = append(actions, HarvestAction{
			Type:   "promote",
			Detail: fmt.Sprintf("%s promoted to golden", input.Task.Model),
		})
	}

	// Sync push if available
	if h.syncPush != nil {
		if err := h.syncPush(ctx); err != nil {
			slog.Warn("harvester sync push failed", "error", err)
		} else {
			actions = append(actions, HarvestAction{Type: "sync_push", Detail: "incremental push"})
		}
	}

	return actions
}

func (h *Harvester) generateNote(input HarvestInput) string {
	if h.tier >= 2 && h.llm != nil {
		note, err := h.generateLLMNote(context.Background(), input)
		if err == nil {
			return note
		}
		slog.Warn("LLM note generation failed, falling back to template", "error", err)
	}
	return h.generateTemplateNote(input)
}

func (h *Harvester) generateTemplateNote(input HarvestInput) string {
	return fmt.Sprintf("%s on %s: %.1f tok/s, TTFT P95 %.0fms, config=%v",
		input.Task.Model, input.Task.Engine,
		input.Result.Throughput, input.Result.TTFTP95,
		input.Result.Config)
}

func (h *Harvester) generateLLMNote(ctx context.Context, input HarvestInput) (string, error) {
	if h.llm == nil {
		return "", fmt.Errorf("no LLM client")
	}
	prompt := fmt.Sprintf(
		"Summarize this benchmark result in 2-3 sentences with actionable insights:\n"+
			"Model: %s, Engine: %s\n"+
			"Throughput: %.1f tok/s, TTFT P95: %.0fms, VRAM: %.0f MiB\n"+
			"Config: %v",
		input.Task.Model, input.Task.Engine,
		input.Result.Throughput, input.Result.TTFTP95, input.Result.VRAMMiB,
		input.Result.Config)
	resp, err := h.llm.ChatCompletion(ctx, []Message{
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestHarvester -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer_harvester.go internal/agent/explorer_harvester_test.go
git commit -m "feat(explorer): add Harvester with template and LLM modes"
```

---

### Task 5: DB Migration — exploration_plans Table

**Files:**
- Modify: `internal/state/sqlite.go` (add migrateV12)

- [ ] **Step 1: Read current migration chain**

Read `internal/state/sqlite.go` to find the latest `migrateVN` and the `migrate()` master function. Identify the current schema version number.

- [ ] **Step 2: Add migrateV12**

Add the following to `internal/state/sqlite.go`:

```go
func (d *DB) migrateV12(ctx context.Context) error {
	var version int
	_ = d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	if version >= 12 {
		return nil
	}

	ddl := `
CREATE TABLE IF NOT EXISTS exploration_plans (
    id            TEXT PRIMARY KEY,
    tier          INTEGER NOT NULL,
    trigger       TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active',
    plan_json     TEXT NOT NULL,
    progress      INTEGER DEFAULT 0,
    total         INTEGER DEFAULT 0,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    completed_at  DATETIME,
    summary_json  TEXT
);
CREATE INDEX IF NOT EXISTS idx_plans_status ON exploration_plans(status);`
	if _, err := d.db.ExecContext(ctx, ddl); err != nil {
		return fmt.Errorf("create exploration_plans table: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, "PRAGMA user_version = 12"); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}
```

Add `migrateV12` call to the `migrate()` master function chain.

- [ ] **Step 3: Add DB methods for exploration_plans**

Add CRUD methods to `internal/state/sqlite.go` (or a new `state/exploration_plan.go` file if sqlite.go is large):

```go
func (d *DB) InsertExplorationPlan(ctx context.Context, plan *ExplorationPlanRow) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO exploration_plans (id, tier, trigger, status, plan_json, progress, total, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		plan.ID, plan.Tier, plan.Trigger, plan.Status, plan.PlanJSON,
		plan.Progress, plan.Total, plan.CreatedAt)
	return err
}

func (d *DB) UpdateExplorationPlan(ctx context.Context, plan *ExplorationPlanRow) error {
	_, err := d.db.ExecContext(ctx,
		`UPDATE exploration_plans SET status=?, progress=?, completed_at=?, summary_json=? WHERE id=?`,
		plan.Status, plan.Progress, plan.CompletedAt, plan.SummaryJSON, plan.ID)
	return err
}

func (d *DB) ListExplorationPlans(ctx context.Context, status string) ([]*ExplorationPlanRow, error) {
	query := `SELECT id, tier, trigger, status, plan_json, progress, total, created_at, completed_at, summary_json FROM exploration_plans`
	var args []any
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT 50`
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []*ExplorationPlanRow
	for rows.Next() {
		p := &ExplorationPlanRow{}
		var completedAt sql.NullTime
		var summaryJSON sql.NullString
		if err := rows.Scan(&p.ID, &p.Tier, &p.Trigger, &p.Status, &p.PlanJSON,
			&p.Progress, &p.Total, &p.CreatedAt, &completedAt, &summaryJSON); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			p.CompletedAt = &completedAt.Time
		}
		if summaryJSON.Valid {
			p.SummaryJSON = summaryJSON.String
		}
		plans = append(plans, p)
	}
	return plans, rows.Err()
}
```

- [ ] **Step 4: Add ExplorationPlanRow type to state/types.go**

```go
type ExplorationPlanRow struct {
	ID          string
	Tier        int
	Trigger     string
	Status      string // "active", "paused", "completed", "archived"
	PlanJSON    string
	Progress    int
	Total       int
	CreatedAt   time.Time
	CompletedAt *time.Time
	SummaryJSON string
}
```

- [ ] **Step 5: Run existing tests to verify no breakage**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/state/ -v -count=1`
Expected: PASS (existing tests still work, new migration is additive)

- [ ] **Step 6: Commit**

```bash
git add internal/state/sqlite.go internal/state/types.go
git commit -m "feat(state): add exploration_plans table (migrateV12)"
```

---

### Task 6: Explorer Orchestrator

**Files:**
- Create: `internal/agent/explorer.go`
- Create: `internal/agent/explorer_test.go`

- [ ] **Step 1: Write Explorer test**

```go
// explorer_test.go
package agent

import (
	"context"
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
				a.mode = toolMode(tt.toolMode)
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
}
```

- [ ] **Step 2: Run test to verify it fails**

- [ ] **Step 3: Implement Explorer**

```go
// explorer.go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ExplorerConfig holds all Explorer configuration.
type ExplorerConfig struct {
	Schedule ScheduleConfig
	Enabled  bool
}

// ExplorerStatus reports the Explorer's current state.
type ExplorerStatus struct {
	Running    bool
	Tier       int
	ActivePlan *ExplorationPlan
	Schedule   ScheduleConfig
	LastRun    time.Time
}

// Explorer orchestrates autonomous knowledge discovery on edge devices.
type Explorer struct {
	config    ExplorerConfig
	agent     *Agent
	explMgr   *ExplorationManager
	db        *state.DB
	bus       *EventBus
	scheduler *Scheduler
	planner   Planner
	harvester *Harvester

	mu         sync.RWMutex
	running    bool
	tier       int
	activePlan *ExplorationPlan
	lastRun    time.Time
	cancel     context.CancelFunc
}

func NewExplorer(config ExplorerConfig, agent *Agent, explMgr *ExplorationManager, db *state.DB, bus *EventBus) *Explorer {
	e := &Explorer{
		config:  config,
		agent:   agent,
		explMgr: explMgr,
		db:      db,
		bus:     bus,
	}
	e.tier = e.detectTier()
	e.scheduler = NewScheduler(config.Schedule, bus)
	e.setupPlanner()
	e.harvester = NewHarvester(e.tier)
	return e
}

func (e *Explorer) detectTier() int {
	if e.agent == nil || !e.agent.Available() {
		return 0
	}
	mode := e.agent.ToolMode()
	if mode == "enabled" {
		return 2
	}
	return 1 // context_only or unknown
}

func (e *Explorer) setupPlanner() {
	if e.tier >= 2 && e.agent != nil {
		e.planner = NewLLMPlanner(e.agent)
	} else {
		e.planner = &RulePlanner{}
	}
}

// Start begins the Explorer's background loops.
func (e *Explorer) Start(ctx context.Context) {
	if !e.config.Enabled {
		slog.Info("explorer disabled")
		return
	}
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	ctx, e.cancel = context.WithCancel(ctx)
	e.running = true
	e.mu.Unlock()

	slog.Info("explorer started", "tier", e.tier)

	// Start scheduler (emits timed events)
	e.scheduler.StartAll(ctx)

	// Main event loop
	ch := e.bus.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			e.handleEvent(ctx, ev)
		}
	}
}

// Stop gracefully shuts down the Explorer.
func (e *Explorer) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.cancel != nil {
		e.cancel()
	}
	e.running = false
}

// Status returns the Explorer's current state.
func (e *Explorer) Status() ExplorerStatus {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return ExplorerStatus{
		Running:    e.running,
		Tier:       e.tier,
		ActivePlan: e.activePlan,
		Schedule:   e.config.Schedule,
		LastRun:    e.lastRun,
	}
}

// Trigger manually triggers a gap scan exploration cycle.
func (e *Explorer) Trigger() {
	e.bus.Publish(ExplorerEvent{Type: EventScheduledGapScan})
}

func (e *Explorer) handleEvent(ctx context.Context, ev ExplorerEvent) {
	slog.Debug("explorer event", "type", ev.Type)

	// Re-detect tier periodically (LLM may have come online/offline)
	newTier := e.detectTier()
	if newTier != e.tier {
		slog.Info("explorer tier changed", "old", e.tier, "new", newTier)
		e.mu.Lock()
		e.tier = newTier
		e.mu.Unlock()
		e.setupPlanner()
		e.harvester = NewHarvester(newTier)
	}

	if e.tier == 0 {
		slog.Debug("explorer: tier 0, skipping event", "type", ev.Type)
		return
	}

	// Build plan input from current state
	input, err := e.buildPlanInput(ctx, &ev)
	if err != nil {
		slog.Warn("explorer: build plan input failed", "error", err)
		return
	}

	// Generate exploration plan
	plan, err := e.planner.Plan(ctx, *input)
	if err != nil {
		slog.Warn("explorer: plan generation failed", "error", err)
		// If LLM planner failed, try rule planner fallback
		if e.tier >= 2 {
			slog.Info("explorer: falling back to RulePlanner")
			rp := &RulePlanner{}
			plan, err = rp.Plan(ctx, *input)
			if err != nil {
				slog.Error("explorer: rule planner also failed", "error", err)
				return
			}
		} else {
			return
		}
	}

	if len(plan.Tasks) == 0 {
		slog.Debug("explorer: no tasks to execute")
		return
	}

	e.mu.Lock()
	e.activePlan = plan
	e.lastRun = time.Now()
	e.mu.Unlock()

	// Persist plan
	if e.db != nil {
		planJSON, _ := json.Marshal(plan)
		_ = e.db.InsertExplorationPlan(ctx, &state.ExplorationPlanRow{
			ID:        plan.ID,
			Tier:      plan.Tier,
			Trigger:   ev.Type,
			Status:    "active",
			PlanJSON:  string(planJSON),
			Total:     len(plan.Tasks),
			CreatedAt: time.Now(),
		})
	}

	// Execute plan tasks sequentially
	e.executePlan(ctx, plan)
}

func (e *Explorer) executePlan(ctx context.Context, plan *ExplorationPlan) {
	for i, task := range plan.Tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Info("explorer: executing task", "kind", task.Kind, "model", task.Model, "engine", task.Engine, "progress", fmt.Sprintf("%d/%d", i+1, len(plan.Tasks)))

		result := e.executeTask(ctx, task)

		// Harvest results
		actions := e.harvester.Harvest(ctx, HarvestInput{Task: task, Result: result})
		for _, a := range actions {
			slog.Info("explorer: harvest action", "type", a.Type, "detail", a.Detail)
		}

		// Update plan progress
		if e.db != nil {
			_ = e.db.UpdateExplorationPlan(ctx, &state.ExplorationPlanRow{
				ID:       plan.ID,
				Status:   "active",
				Progress: i + 1,
			})
		}
	}

	// Mark plan completed
	e.mu.Lock()
	e.activePlan = nil
	e.mu.Unlock()
	if e.db != nil {
		now := time.Now()
		_ = e.db.UpdateExplorationPlan(ctx, &state.ExplorationPlanRow{
			ID:          plan.ID,
			Status:      "completed",
			Progress:    len(plan.Tasks),
			CompletedAt: &now,
		})
	}
}

func (e *Explorer) executeTask(ctx context.Context, task PlanTask) HarvestResult {
	if e.explMgr == nil {
		return HarvestResult{Success: false, Error: "no exploration manager"}
	}

	req := ExplorationStart{
		Kind:   task.Kind,
		Target: ExplorationTarget{Model: task.Model, Engine: task.Engine},
	}
	if task.Params != nil {
		req.SearchSpace = task.Params
	}

	status, err := e.explMgr.StartAndWait(ctx, req)
	if err != nil {
		return HarvestResult{Success: false, Error: err.Error()}
	}

	if status.Run.Status == "failed" {
		return HarvestResult{Success: false, Error: status.Run.Error}
	}

	// Parse benchmark results from exploration summary
	return e.parseExplorationResult(status)
}

func (e *Explorer) parseExplorationResult(status *ExplorationStatus) HarvestResult {
	result := HarvestResult{Success: true}
	// Parse summary JSON for throughput/latency data
	if status.Run.SummaryJSON != "" {
		var summary map[string]any
		if err := json.Unmarshal([]byte(status.Run.SummaryJSON), &summary); err == nil {
			if tp, ok := summary["throughput_tps"].(float64); ok {
				result.Throughput = tp
			}
			if ttft, ok := summary["ttft_p95_ms"].(float64); ok {
				result.TTFTP95 = ttft
			}
		}
	}
	return result
}

func (e *Explorer) buildPlanInput(ctx context.Context, ev *ExplorerEvent) (*PlanInput, error) {
	input := &PlanInput{Event: ev}

	// Gather gaps, deploys, advisories from DB/tools
	// This will be wired through ToolDeps in the integration task.
	// For now, return minimal input — event-specific data.

	return input, nil
}
```

Note: `buildPlanInput` is a skeleton that will be fully wired in the integration phase (Task 8). The key architecture is established here.

- [ ] **Step 4: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestExplorer -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/agent/explorer.go internal/agent/explorer_test.go
git commit -m "feat(explorer): add Explorer orchestrator with tier detection and event loop"
```

---

### Task 7: Patrol → EventBus Bridge

**Files:**
- Modify: `internal/agent/patrol.go`

- [ ] **Step 1: Read patrol.go**

Read the current `patrol.go` to find where alerts are generated and where to inject EventBus calls.

- [ ] **Step 2: Add EventBus option to Patrol**

Add a new `PatrolOption` and emit events when alerts are created:

```go
// Add to patrol.go

func WithEventBus(bus *EventBus) PatrolOption {
	return func(p *Patrol) { p.eventBus = bus }
}

// Add field to Patrol struct:
// eventBus *EventBus

// In checkMetrics() or reactToAlerts(), after creating an alert:
func (p *Patrol) emitEvent(alert Alert) {
	if p.eventBus == nil {
		return
	}
	var eventType string
	switch alert.Type {
	case "deploy_crash":
		eventType = EventPatrolOOM // OOM is most common crash cause
	case "gpu_idle":
		eventType = EventPatrolIdle
	default:
		return // not all alerts trigger exploration
	}
	p.eventBus.Publish(ExplorerEvent{
		Type:    eventType,
		AlertID: alert.ID,
	})
}
```

Call `p.emitEvent(alert)` in the appropriate places where alerts are persisted.

- [ ] **Step 3: Run existing patrol tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestPatrol -v`
Expected: PASS (new option is additive, doesn't break existing behavior)

- [ ] **Step 4: Commit**

```bash
git add internal/agent/patrol.go
git commit -m "feat(patrol): emit events to EventBus on OOM and idle alerts"
```

---

### Task 8: MCP Tools + CLI + Wiring

**Files:**
- Create: `internal/mcp/tools_explorer.go`
- Modify: `internal/mcp/tools_deps.go`
- Create: `cmd/aima/explorer.go`
- Modify: `cmd/aima/main.go`

- [ ] **Step 1: Add Explorer fields to ToolDeps**

In `internal/mcp/tools_deps.go`, add:

```go
// Explorer
ExplorerStatus  func(ctx context.Context) (json.RawMessage, error)
ExplorerConfig  func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)
ExplorerTrigger func(ctx context.Context) (json.RawMessage, error)
```

- [ ] **Step 2: Create tools_explorer.go**

```go
// internal/mcp/tools_explorer.go
package mcp

// Register explorer tools in the tool definitions list.
// Follow the same pattern as tools_patrol.go or tools_integration.go:
// - Define tool schema (name, description, inputSchema)
// - Wire handler to deps.ExplorerStatus/Config/Trigger
```

Define 3 tools:
- `explorer.status` — no params, returns ExplorerStatus JSON
- `explorer.config` — optional params to update schedule config
- `explorer.trigger` — no params, triggers manual exploration

- [ ] **Step 3: Create CLI commands**

```go
// cmd/aima/explorer.go
package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

func newExplorerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explorer",
		Short: "Knowledge exploration automation",
	}
	cmd.AddCommand(
		newExplorerStatusCmd(),
		newExplorerTriggerCmd(),
		newExplorerConfigCmd(),
	)
	return cmd
}

func newExplorerStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show explorer status",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Call deps.ExplorerStatus via MCP tool pattern
			return nil
		},
	}
}

// ... trigger and config commands follow same thin CLI pattern
```

- [ ] **Step 4: Wire Explorer in main.go**

In `cmd/aima/main.go`:

1. Create EventBus in initialization
2. Create Explorer with agent, ExplorationManager, DB, EventBus
3. Start Explorer in background goroutine
4. Pass EventBus to Patrol via `WithEventBus(bus)`
5. Wire ExplorerStatus/Config/Trigger in buildToolDeps

```go
// In main.go initialization:
eventBus := agent.NewEventBus()

explorer := agent.NewExplorer(agent.ExplorerConfig{
    Schedule: agent.DefaultScheduleConfig(),
    Enabled:  true,
}, goAgent, explMgr, db, eventBus)

go explorer.Start(ctx)

// In NewPatrol call, add:
agent.WithEventBus(eventBus),
```

- [ ] **Step 5: Run full test suite**

Run: `cd /Users/jguan/projects/AIMA && go build ./cmd/aima && go test ./... -count=1`
Expected: BUILD SUCCESS + all tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/mcp/tools_explorer.go internal/mcp/tools_deps.go cmd/aima/explorer.go cmd/aima/main.go
git commit -m "feat(explorer): add MCP tools, CLI commands, and wire Explorer in main"
```

---

### Task 9: LLMPlanner (Tier 2)

**Files:**
- Create: `internal/agent/explorer_llmplanner.go`
- Create: `internal/agent/explorer_llmplanner_test.go`

- [ ] **Step 1: Write LLMPlanner test with mock LLM**

```go
// explorer_llmplanner_test.go
package agent

import (
	"context"
	"testing"
)

func TestLLMPlanner_ParsesStructuredResponse(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{
			Content: `{"tasks":[{"kind":"validate","model":"qwen3-8b","engine":"vllm","reason":"test baseline"}]}`,
		}},
	}
	p := NewLLMPlanner(NewAgent(llm, &mockTools{}))
	plan, err := p.Plan(context.Background(), PlanInput{
		Hardware: HardwareInfo{Profile: "nvidia-rtx4090-x86", VRAMMiB: 24576},
		Gaps:     []GapEntry{{Model: "qwen3-8b", Engine: "vllm"}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Tier != 2 {
		t.Errorf("tier = %d, want 2", plan.Tier)
	}
	if len(plan.Tasks) == 0 {
		t.Fatal("expected tasks from LLM response")
	}
}

func TestLLMPlanner_FallbackOnInvalidJSON(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "I can't generate a plan right now."}},
	}
	p := NewLLMPlanner(NewAgent(llm, &mockTools{}))
	_, err := p.Plan(context.Background(), PlanInput{})
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}
```

- [ ] **Step 2: Implement LLMPlanner**

```go
// explorer_llmplanner.go
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// LLMPlanner generates exploration plans using LLM reasoning (Tier 2).
type LLMPlanner struct {
	agent *Agent
}

func NewLLMPlanner(agent *Agent) *LLMPlanner {
	return &LLMPlanner{agent: agent}
}

func (p *LLMPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorationPlan, error) {
	prompt := buildPlannerPrompt(input)
	resp, err := p.agent.llm.ChatCompletion(ctx, []Message{
		{Role: "system", Content: llmPlannerSystemPrompt},
		{Role: "user", Content: prompt},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("LLM plan generation: %w", err)
	}
	return parsePlanResponse(resp.Content)
}

const llmPlannerSystemPrompt = `You are an AI inference optimization planner. Given device hardware info, knowledge gaps, and deployment state, generate an exploration plan as JSON.

Output format:
{"tasks":[{"kind":"validate|tune|open_question|compare","model":"...","engine":"...","params":{},"reason":"...","priority":0}]}

Rules:
- Prioritize deployed models without benchmarks (kind=validate)
- For tune tasks, suggest specific parameter ranges based on hardware VRAM
- Consider central advisories and validate them
- Max 5 tasks per plan
- Be specific about WHY each task matters`

func buildPlannerPrompt(input PlanInput) string {
	data, _ := json.MarshalIndent(map[string]any{
		"hardware":       input.Hardware,
		"gaps":           input.Gaps,
		"active_deploys": input.ActiveDeploys,
		"advisories":     input.Advisories,
		"open_questions": input.OpenQuestions,
		"event":          input.Event,
	}, "", "  ")
	return string(data)
}

func parsePlanResponse(content string) (*ExplorationPlan, error) {
	var parsed struct {
		Tasks []PlanTask `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return nil, fmt.Errorf("parse LLM plan response: %w", err)
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano()))))[:8]
	return &ExplorationPlan{
		ID:        id,
		Tier:      2,
		Tasks:     parsed.Tasks,
		Reasoning: content,
	}, nil
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -run TestLLMPlanner -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/explorer_llmplanner.go internal/agent/explorer_llmplanner_test.go
git commit -m "feat(explorer): add LLMPlanner for Tier 2 intelligent exploration"
```

---

### Task 10: buildPlanInput Wiring + Integration Test

**Files:**
- Modify: `internal/agent/explorer.go` (flesh out buildPlanInput)
- Create: `internal/agent/explorer_integration_test.go`

- [ ] **Step 1: Wire buildPlanInput to gather real data**

Update `explorer.go`'s `buildPlanInput` to use tool calls for gaps, deploys, etc.:

```go
func (e *Explorer) buildPlanInput(ctx context.Context, ev *ExplorerEvent) (*PlanInput, error) {
	input := &PlanInput{Event: ev}

	// Gather gaps via knowledge.gaps tool
	if e.gatherGaps != nil {
		gaps, err := e.gatherGaps(ctx)
		if err == nil {
			input.Gaps = gaps
		}
	}

	// Gather active deploys
	if e.gatherDeploys != nil {
		deploys, err := e.gatherDeploys(ctx)
		if err == nil {
			input.ActiveDeploys = deploys
		}
	}

	// Recent exploration history
	if e.db != nil {
		runs, _ := e.db.ListExplorationRuns(ctx, 10)
		for _, r := range runs {
			input.History = append(input.History, *r)
		}
	}

	return input, nil
}
```

Add `gatherGaps` and `gatherDeploys` as function fields on Explorer, wired via options or `buildToolDeps`.

- [ ] **Step 2: Write integration test**

Test that Explorer starts, receives an event, generates a plan, and attempts execution (with mocked tools).

- [ ] **Step 3: Run full test suite**

Run: `cd /Users/jguan/projects/AIMA && go test ./internal/agent/ -v -count=1`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/agent/explorer.go internal/agent/explorer_integration_test.go
git commit -m "feat(explorer): wire buildPlanInput data gathering + integration test"
```

---

### Task 11: Final Build Verification

- [ ] **Step 1: Build all binaries**

```bash
cd /Users/jguan/projects/AIMA
go build ./cmd/aima
go build ./cmd/central
go vet ./...
```
Expected: BUILD SUCCESS, no vet warnings

- [ ] **Step 2: Run full test suite with race detector**

```bash
go test -race ./internal/agent/ -v -count=1
go test -race ./internal/state/ -v -count=1
go test -race ./internal/mcp/ -v -count=1
```
Expected: PASS, no race conditions

- [ ] **Step 3: Commit any fixups**

```bash
git add -A && git commit -m "fix: address build and race issues from explorer integration"
```
