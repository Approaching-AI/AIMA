package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	state "github.com/jguan/aima/internal"
)

// ExplorerConfig holds all Explorer configuration.
type ExplorerConfig struct {
	Schedule ScheduleConfig
	Enabled  bool
}

// ExplorerStatus reports the Explorer's current state.
type ExplorerStatus struct {
	Running    bool            `json:"running"`
	Tier       int             `json:"tier"`
	ActivePlan *ExplorerPlan   `json:"active_plan,omitempty"`
	Schedule   ScheduleConfig  `json:"schedule"`
	LastRun    time.Time       `json:"last_run,omitempty"`
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

	// Data gathering functions, wired via options or buildToolDeps.
	gatherGaps    func(ctx context.Context) ([]GapEntry, error)
	gatherDeploys func(ctx context.Context) ([]DeployStatus, error)

	// Advisory feedback callback, wired via WithAdvisoryFeedback.
	advisoryFeedback func(ctx context.Context, advisoryID, status, reason string) error

	mu         sync.RWMutex
	running    bool
	tier       int
	activePlan *ExplorerPlan
	lastRun    time.Time
	cancel     context.CancelFunc
}

// ExplorerOption configures the Explorer.
type ExplorerOption func(*Explorer)

// WithGatherGaps sets the function to gather knowledge gaps.
func WithGatherGaps(fn func(ctx context.Context) ([]GapEntry, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherGaps = fn }
}

// WithGatherDeploys sets the function to gather active deployments.
func WithGatherDeploys(fn func(ctx context.Context) ([]DeployStatus, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherDeploys = fn }
}

// WithAdvisoryFeedback sets the callback for sending advisory feedback to central.
func WithAdvisoryFeedback(fn func(ctx context.Context, advisoryID, status, reason string) error) ExplorerOption {
	return func(e *Explorer) { e.advisoryFeedback = fn }
}

func NewExplorer(config ExplorerConfig, agent *Agent, explMgr *ExplorationManager, db *state.DB, bus *EventBus, opts ...ExplorerOption) *Explorer {
	e := &Explorer{
		config:  config,
		agent:   agent,
		explMgr: explMgr,
		db:      db,
		bus:     bus,
	}
	for _, o := range opts {
		o(e)
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

	// Handle central advisory/scenario events directly (even at tier 0)
	switch ev.Type {
	case EventCentralAdvisory:
		e.handleAdvisory(ctx, ev)
		return
	case EventCentralScenario:
		e.handleScenario(ctx, ev)
		return
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

// handleAdvisory processes a central advisory event: parse advisory,
// create a validation task, execute it, and send feedback to central.
func (e *Explorer) handleAdvisory(ctx context.Context, ev ExplorerEvent) {
	if len(ev.Advisory) == 0 {
		slog.Warn("explorer: advisory event with empty payload")
		return
	}

	var adv struct {
		ID       string `json:"id"`
		Model    string `json:"model"`
		Engine   string `json:"engine"`
		Hardware string `json:"hardware"`
		Type     string `json:"type"`
		Title    string `json:"title"`
		Details  string `json:"details"`
	}
	if err := json.Unmarshal(ev.Advisory, &adv); err != nil {
		slog.Warn("explorer: parse advisory", "error", err)
		return
	}

	slog.Info("explorer: received central advisory",
		"id", adv.ID, "type", adv.Type, "model", adv.Model, "engine", adv.Engine)

	// If no exploration manager, just log and send feedback that we can't validate
	if e.explMgr == nil || e.tier == 0 {
		slog.Info("explorer: cannot validate advisory (no exploration manager or tier 0)")
		e.sendAdvisoryFeedback(ctx, adv.ID, "deferred", "no exploration capability on this device")
		return
	}

	// Create and execute a validation task
	task := PlanTask{
		Kind:   "validate",
		Model:  adv.Model,
		Engine: adv.Engine,
		Reason: fmt.Sprintf("validate central advisory %s", adv.ID),
	}

	result := e.executeTask(ctx, task)

	// Send feedback based on result
	if result.Success {
		reason := fmt.Sprintf("validated: %.1f tok/s, TTFT P95 %.0fms", result.Throughput, result.TTFTP95)
		e.sendAdvisoryFeedback(ctx, adv.ID, "accepted", reason)
	} else {
		e.sendAdvisoryFeedback(ctx, adv.ID, "rejected", "validation failed: "+result.Error)
	}

	// Harvest results as usual
	actions := e.harvester.Harvest(ctx, HarvestInput{Task: task, Result: result})
	for _, a := range actions {
		slog.Info("explorer: advisory harvest action", "type", a.Type, "detail", a.Detail)
	}
}

// handleScenario processes a central scenario event. Currently logs and defers.
func (e *Explorer) handleScenario(ctx context.Context, ev ExplorerEvent) {
	if len(ev.Advisory) == 0 {
		return
	}

	var scenario struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(ev.Advisory, &scenario); err != nil {
		slog.Warn("explorer: parse scenario", "error", err)
		return
	}

	slog.Info("explorer: received central scenario",
		"id", scenario.ID, "name", scenario.Name)
}

func (e *Explorer) sendAdvisoryFeedback(ctx context.Context, advisoryID, status, reason string) {
	if e.advisoryFeedback == nil {
		slog.Debug("explorer: no advisory feedback callback, skipping")
		return
	}
	if err := e.advisoryFeedback(ctx, advisoryID, status, reason); err != nil {
		slog.Warn("explorer: advisory feedback failed",
			"advisory_id", advisoryID, "error", err)
	}
}

func (e *Explorer) executePlan(ctx context.Context, plan *ExplorerPlan) {
	for i, task := range plan.Tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		slog.Info("explorer: executing task",
			"kind", task.Kind, "model", task.Model, "engine", task.Engine,
			"progress", fmt.Sprintf("%d/%d", i+1, len(plan.Tasks)))

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

	// Convert PlanTask params (map[string]any) to ExplorationStart.SearchSpace (map[string][]any)
	var searchSpace map[string][]any
	if task.Params != nil {
		searchSpace = make(map[string][]any)
		for k, v := range task.Params {
			searchSpace[k] = []any{v}
		}
	}

	req := ExplorationStart{
		Kind:   task.Kind,
		Target: ExplorationTarget{Model: task.Model, Engine: task.Engine},
	}
	if searchSpace != nil {
		req.SearchSpace = searchSpace
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
		runs, _ := e.db.ListExplorationRuns(ctx, "", 10)
		for _, r := range runs {
			input.History = append(input.History, *r)
		}
	}

	return input, nil
}
