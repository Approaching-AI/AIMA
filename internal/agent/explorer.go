package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	state "github.com/jguan/aima/internal"
)

// ExplorerConfig holds all Explorer configuration.
type ExplorerConfig struct {
	Schedule        ScheduleConfig
	Enabled         bool
	Mode            string        // "continuous" | "once" | "budget"
	MaxRounds       int           // budget mode: max plans to execute (0=unlimited)
	MaxPlanDuration time.Duration // per-plan time budget (default 30min)
	MaxTokensPerDay int           // daily LLM token cap (0=unlimited)
}

// ExplorerStatus reports the Explorer's current state.
type ExplorerStatus struct {
	Running         bool           `json:"running"`
	Enabled         bool           `json:"enabled"`
	Tier            int            `json:"tier"`
	ActivePlan      *ExplorerPlan  `json:"active_plan,omitempty"`
	Schedule        ScheduleConfig `json:"schedule"`
	LastRun         time.Time      `json:"last_run,omitempty"`
	Mode            string         `json:"mode"`
	RoundsUsed      int            `json:"rounds_used"`
	MaxRounds       int            `json:"max_rounds"`
	TokensUsedToday int            `json:"tokens_used_today"`
	MaxTokensPerDay int            `json:"max_tokens_per_day"`
	LastPlanMetrics *PlanMetrics   `json:"last_plan_metrics,omitempty"`
}

// PlanMetrics captures per-plan execution statistics.
type PlanMetrics struct {
	TotalTasks       int     `json:"total_tasks"`
	Completed        int     `json:"completed"`
	Failed           int     `json:"failed"`
	Skipped          int     `json:"skipped"`
	DiscoveryCount   int     `json:"discovery_count"`
	DurationS        float64 `json:"duration_s"`
	SuccessRate      float64 `json:"success_rate"`
	AvgTaskDurationS float64 `json:"avg_task_duration_s"`
	TokensUsed       int     `json:"tokens_used"`
}

// maxExplorationFailures is the threshold after which a model+engine combo
// is considered permanently broken and excluded from future plans.
const maxExplorationFailures = 2

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
	gatherHardware      func(ctx context.Context) (HardwareInfo, error)
	gatherGaps          func(ctx context.Context) ([]GapEntry, error)
	gatherDeploys       func(ctx context.Context) ([]DeployStatus, error)
	gatherOpenQuestions func(ctx context.Context) ([]OpenQuestion, error)
	gatherAdvisories    func(ctx context.Context) ([]Advisory, error)
	gatherLocalModels   func(ctx context.Context) ([]LocalModel, error)
	gatherLocalEngines  func(ctx context.Context) ([]LocalEngine, error)

	// Harvester callbacks, wired via options or buildToolDeps.
	syncPush func(ctx context.Context) error
	saveNote func(ctx context.Context, title, content, hardware, model, engine string) error

	// Advisory feedback callback, wired via WithAdvisoryFeedback.
	advisoryFeedback func(ctx context.Context, advisoryID, status, reason string) error

	mu               sync.RWMutex
	running          bool
	tier             int
	activePlan       *ExplorerPlan
	cachedGPUArch    string // cached from gatherHardware for overlay YAML (O13)
	lastRun          time.Time
	cancel context.CancelFunc

	// T2: Resource control state
	roundsUsed      int
	tokensUsedToday int
	tokenResetDate  string // "2006-01-02"

	// T5: Plan metrics
	lastPlanMetrics *PlanMetrics
}

// ExplorerOption configures the Explorer.
type ExplorerOption func(*Explorer)

// WithGatherGaps sets the function to gather knowledge gaps.
func WithGatherGaps(fn func(ctx context.Context) ([]GapEntry, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherGaps = fn }
}

// WithGatherHardware sets the function to gather hardware context.
func WithGatherHardware(fn func(ctx context.Context) (HardwareInfo, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherHardware = fn }
}

// WithGatherDeploys sets the function to gather active deployments.
func WithGatherDeploys(fn func(ctx context.Context) ([]DeployStatus, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherDeploys = fn }
}

// WithGatherOpenQuestions sets the function to gather open questions.
func WithGatherOpenQuestions(fn func(ctx context.Context) ([]OpenQuestion, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherOpenQuestions = fn }
}

// WithExplorerSyncPush sets the callback for sync push after successful harvest.
func WithExplorerSyncPush(fn func(ctx context.Context) error) ExplorerOption {
	return func(e *Explorer) { e.syncPush = fn }
}

// WithExplorerSaveNote sets the callback for durable knowledge-note persistence.
func WithExplorerSaveNote(fn func(ctx context.Context, title, content, hardware, model, engine string) error) ExplorerOption {
	return func(e *Explorer) { e.saveNote = fn }
}

// WithGatherAdvisories sets the function to gather pending advisories from central.
func WithGatherAdvisories(fn func(ctx context.Context) ([]Advisory, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherAdvisories = fn }
}

// WithGatherLocalModels sets the function to list locally available models.
func WithGatherLocalModels(fn func(ctx context.Context) ([]LocalModel, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherLocalModels = fn }
}

// WithGatherLocalEngines sets the function to list locally installed engines with metadata.
func WithGatherLocalEngines(fn func(ctx context.Context) ([]LocalEngine, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherLocalEngines = fn }
}

// WithAdvisoryFeedback sets the callback for sending advisory feedback to central.
func WithAdvisoryFeedback(fn func(ctx context.Context, advisoryID, status, reason string) error) ExplorerOption {
	return func(e *Explorer) { e.advisoryFeedback = fn }
}

// WithRoundsUsed restores the rounds counter from persisted state on restart.
func WithRoundsUsed(n int) ExplorerOption {
	return func(e *Explorer) { e.roundsUsed = n }
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
	e.config.Schedule = normalizeScheduleConfig(e.config.Schedule)
	e.tier = e.detectTier()
	e.scheduler = NewScheduler(e.config.Schedule, bus)
	e.config.Schedule = e.scheduler.Config()
	e.setupPlannerLocked()
	e.harvester = e.buildHarvesterLocked()
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

func (e *Explorer) setupPlannerLocked() {
	if e.tier >= 2 && e.agent != nil {
		e.planner = NewLLMPlanner(e.agent)
	} else {
		e.planner = &RulePlanner{}
	}
}

func (e *Explorer) buildHarvesterLocked() *Harvester {
	opts := make([]HarvesterOption, 0, 4)
	if e.tier >= 2 && e.agent != nil && e.agent.llm != nil {
		opts = append(opts, WithHarvesterLLM(e.agent.llm))
	}
	if e.syncPush != nil {
		opts = append(opts, WithSyncPush(e.syncPush))
	}
	if e.saveNote != nil {
		opts = append(opts, WithSaveNote(e.saveNote))
	}
	// T6: Wire token callback so harvester LLM calls accumulate into Explorer budget
	opts = append(opts, WithTokenCallback(func(tokens int) {
		e.mu.Lock()
		e.tokensUsedToday += tokens
		e.mu.Unlock()
	}))
	return NewHarvester(e.tier, opts...)
}

// Start begins the Explorer's background loops.
func (e *Explorer) Start(ctx context.Context) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	ctx, e.cancel = context.WithCancel(ctx)
	e.running = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running = false
		e.activePlan = nil
		e.mu.Unlock()
	}()

	if e.isEnabled() {
		slog.Info("explorer started", "tier", e.tier)
	} else {
		slog.Info("explorer started in disabled mode", "tier", e.tier)
	}

	// Start scheduler (emits timed events)
	e.scheduler.StartAll(ctx)

	// Main event loop
	ch := e.bus.Subscribe()
	defer e.bus.Unsubscribe(ch)
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
		Running:         e.running,
		Enabled:         e.config.Enabled,
		Tier:            e.tier,
		ActivePlan:      e.activePlan,
		Schedule:        e.config.Schedule,
		LastRun:         e.lastRun,
		Mode:            e.config.Mode,
		RoundsUsed:      e.roundsUsed,
		MaxRounds:       e.config.MaxRounds,
		TokensUsedToday: e.tokensUsedToday,
		MaxTokensPerDay: e.config.MaxTokensPerDay,
		LastPlanMetrics:  e.lastPlanMetrics,
	}
}

func (e *Explorer) isEnabled() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.config.Enabled
}

func (e *Explorer) currentPlanner() Planner {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.planner
}

func (e *Explorer) currentHarvester() *Harvester {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.harvester
}

// Trigger manually triggers a gap scan exploration cycle.
func (e *Explorer) Trigger() {
	e.bus.Publish(ExplorerEvent{Type: EventScheduledGapScan})
}

func (e *Explorer) handleEvent(ctx context.Context, ev ExplorerEvent) {
	slog.Info("explorer event received", "type", ev.Type)

	// Re-detect tier periodically (LLM may have come online/offline)
	if e.refreshTier(ctx) {
		e.mu.RLock()
		currentTier := e.tier
		e.mu.RUnlock()
		slog.Info("explorer tier changed", "new", currentTier)
	}

	if !e.isEnabled() {
		slog.Debug("explorer disabled, skipping event", "type", ev.Type)
		return
	}

	// T2: Mode and budget checks
	e.mu.Lock()
	today := time.Now().Format("2006-01-02")
	if e.tokenResetDate != today {
		e.tokensUsedToday = 0
		if e.config.Mode == "budget" {
			e.roundsUsed = 0
		}
		e.tokenResetDate = today
	}
	mode := e.config.Mode
	maxRounds := e.config.MaxRounds
	maxTokens := e.config.MaxTokensPerDay
	roundsUsed := e.roundsUsed
	tokensUsed := e.tokensUsedToday
	e.mu.Unlock()

	if mode == "once" && roundsUsed >= 1 {
		e.mu.Lock()
		e.config.Enabled = false
		e.mu.Unlock()
		slog.Info("explorer: once mode completed, auto-disabling")
		return
	}
	if mode == "budget" && maxRounds > 0 && roundsUsed >= maxRounds {
		slog.Info("explorer: budget exhausted", "rounds_used", roundsUsed, "max_rounds", maxRounds)
		return
	}
	if maxTokens > 0 && tokensUsed >= maxTokens {
		slog.Info("explorer: daily token budget exhausted", "used", tokensUsed, "max", maxTokens)
		return
	}

	// Handle central advisory/scenario events directly (even when normal planning is skipped)
	switch ev.Type {
	case EventCentralAdvisory:
		e.handleAdvisory(ctx, ev)
		return
	case EventCentralScenario:
		e.handleScenario(ctx, ev)
		return
	}

	e.mu.RLock()
	tier := e.tier
	e.mu.RUnlock()
	if tier == 0 {
		slog.Debug("explorer: tier 0, skipping event", "type", ev.Type)
		return
	}

	// Build plan input from current state
	slog.Info("explorer: building plan input")
	input, err := e.buildPlanInput(ctx, &ev)
	if err != nil {
		slog.Warn("explorer: build plan input failed", "error", err)
		return
	}
	slog.Info("explorer: plan input ready",
		"gaps", len(input.Gaps), "deploys", len(input.ActiveDeploys),
		"models", len(input.LocalModels), "engines", len(input.LocalEngines),
		"history", len(input.History), "hw", input.Hardware.Profile)

	// Generate exploration plan
	planner := e.currentPlanner()
	slog.Info("explorer: generating plan", "tier", tier)
	plan, planTokens, err := planner.Plan(ctx, *input)
	degraded := false
	if err != nil {
		slog.Warn("explorer: plan generation failed", "error", err)
		// If LLM planner failed, try rule planner fallback
		if tier >= 2 {
			slog.Info("explorer: LLM unavailable, degrading to Tier 1 planner")
			rp := &RulePlanner{}
			plan, planTokens, err = rp.Plan(ctx, *input)
			if err != nil {
				slog.Error("explorer: rule planner also failed", "error", err)
				return
			}
			degraded = true
		} else {
			return
		}
	}
	if degraded {
		plan.Reasoning = "rule-based (degraded from Tier 2)"
	}

	// T6: Track planner token usage
	if planTokens > 0 {
		e.mu.Lock()
		e.tokensUsedToday += planTokens
		e.mu.Unlock()
	}

	// T3: DB-based dedup (replaces history-slice dedup in planners)
	if e.db != nil {
		var dedupFiltered []PlanTask
		for _, t := range plan.Tasks {
			if t.SourceRef != "" {
				dedupFiltered = append(dedupFiltered, t)
				continue
			}
			completed, _ := e.db.HasCompletedExploration(ctx, t.Model, t.Engine)
			if completed {
				slog.Info("explorer: dedup skipped (completed)", "model", t.Model, "engine", t.Engine)
				continue
			}
			failCount, _ := e.db.CountFailedExplorations(ctx, t.Model, t.Engine)
			if failCount >= maxExplorationFailures {
				slog.Info("explorer: dedup skipped (too many failures)", "model", t.Model, "engine", t.Engine, "fails", failCount)
				continue
			}
			dedupFiltered = append(dedupFiltered, t)
		}
		plan.Tasks = dedupFiltered
	}

	slog.Info("explorer: plan generated", "tasks", len(plan.Tasks), "id", plan.ID, "tier", plan.Tier)
	if len(plan.Tasks) == 0 {
		slog.Info("explorer: no tasks to execute after filtering")
		// N1: empty plan still counts as a budget round — prevents infinite
		// LLM calls when all proposed tasks are deduped.
		e.mu.Lock()
		e.roundsUsed++
		e.mu.Unlock()
		return
	}

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

	// D1: synchronous execution — budget, dedup, and activePlan are naturally correct
	e.mu.Lock()
	e.activePlan = plan
	e.lastRun = time.Now()
	e.roundsUsed++ // increment BEFORE execution to prevent race
	e.mu.Unlock()

	// Plan time budget
	maxDur := e.config.MaxPlanDuration
	if maxDur <= 0 {
		maxDur = 30 * time.Minute
	}
	planCtx, planCancel := context.WithTimeout(ctx, maxDur)

	e.mu.RLock()
	tokensBefore := e.tokensUsedToday
	e.mu.RUnlock()

	planStart := time.Now()
	e.executePlan(planCtx, plan)
	planCancel()
	elapsed := time.Since(planStart)

	// D8: refresh tier after execution (LLM may have gone offline)
	e.refreshTier(ctx)

	e.mu.Lock()
	tokensAfter := e.tokensUsedToday
	e.mu.Unlock()

	metrics := e.computePlanMetrics(plan, elapsed, tokensAfter-tokensBefore)
	e.mu.Lock()
	e.lastPlanMetrics = metrics
	e.activePlan = nil // D9: clear after execution
	e.mu.Unlock()

	// D10: log what's still deployed when budget exhausted
	if mode == "budget" && maxRounds > 0 && e.roundsUsed >= maxRounds {
		slog.Info("explorer: budget exhausted — any active deployments remain running")
	}
}

// handleAdvisory processes a central advisory event: parse advisory,
// create a validation task, execute it, and send feedback to central.
func (e *Explorer) handleAdvisory(ctx context.Context, ev ExplorerEvent) {
	if len(ev.Advisory) == 0 {
		slog.Warn("explorer: advisory event with empty payload")
		return
	}

	defaultHardware := ""
	if hw, err := e.currentHardware(ctx); err == nil {
		defaultHardware = firstTaskHardware(hw.Profile, hw.GPUArch)
	}
	advisory, task, err := parseAdvisoryTask(ev.Advisory, defaultHardware)
	if err != nil {
		slog.Warn("explorer: parse advisory", "error", err)
		return
	}

	slog.Info("explorer: received central advisory",
		"id", advisory.ID, "type", advisory.Type, "model", task.Model, "engine", task.Engine)

	// If no exploration manager, just log and send feedback that we can't validate
	if e.explMgr == nil || e.tier == 0 {
		slog.Info("explorer: cannot validate advisory (no exploration manager or tier 0)")
		e.sendAdvisoryFeedback(ctx, advisory.ID, "deferred", "no exploration capability on this device")
		return
	}

	harvester := e.currentHarvester()
	go func() {
		result := e.executeTask(ctx, task, "") // advisory tasks aren't from plans

		if result.Success {
			reason := fmt.Sprintf("validated: %.1f tok/s, TTFT P95 %.0fms", result.Throughput, result.TTFTP95)
			e.sendAdvisoryFeedback(ctx, advisory.ID, "accepted", reason)
		} else {
			e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", "validation failed: "+result.Error)
		}

		actions := harvester.Harvest(ctx, HarvestInput{Task: task, Result: result})
		for _, a := range actions {
			slog.Info("explorer: advisory harvest action", "type", a.Type, "detail", a.Detail)
		}
	}()
}

// handleScenario processes a central scenario event: parses the scenario,
// checks feasibility against local hardware, and persists a knowledge note.
func (e *Explorer) handleScenario(ctx context.Context, ev ExplorerEvent) {
	if len(ev.Advisory) == 0 {
		return
	}

	var scenario struct {
		ID       string   `json:"id"`
		Name     string   `json:"name"`
		Hardware string   `json:"hardware_profile"`
		Models   []string `json:"models"`
		Source   string   `json:"source"`
	}
	if err := json.Unmarshal(ev.Advisory, &scenario); err != nil {
		slog.Warn("explorer: parse scenario", "error", err)
		return
	}

	slog.Info("explorer: received central scenario",
		"id", scenario.ID, "name", scenario.Name, "models", len(scenario.Models))

	// Check feasibility: compare scenario hardware target against local hardware
	hw, err := e.currentHardware(ctx)
	if err != nil {
		slog.Debug("explorer: cannot check scenario feasibility", "error", err)
		return
	}

	match := scenario.Hardware == "" || scenario.Hardware == hw.Profile || scenario.Hardware == hw.GPUArch
	slog.Info("explorer: scenario feasibility",
		"scenario", scenario.Name, "target_hw", scenario.Hardware,
		"local_hw", hw.Profile, "feasible", match)

	// Persist a knowledge note about the received scenario
	if e.saveNote != nil {
		note := fmt.Sprintf("Received scenario %q from central (source=%s, models=%v, feasible=%v)",
			scenario.Name, scenario.Source, scenario.Models, match)
		_ = e.saveNote(ctx, "central scenario received", note, hw.Profile, "", "")
	}
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
	harvester := e.currentHarvester()
	// Track deploy-level failures so we can skip doomed tasks within the same plan.
	// Key: "model|engine", only set for deploy crashes (not benchmark/param failures).
	deployFailures := make(map[string]string) // key → error message
	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		select {
		case <-ctx.Done():
			// T5: mark remaining tasks as skipped_timeout
			for j := i; j < len(plan.Tasks); j++ {
				if plan.Tasks[j].Status == "" {
					plan.Tasks[j].Status = "skipped_timeout"
				}
			}
			return
		default:
		}

		// Intra-plan feedback: skip tasks whose model+engine already crashed
		// during deployment in this plan (e.g., OOM, exit crash). Tune failures
		// are param-specific and don't block other param combinations.
		taskKey := task.Model + "|" + task.Engine
		if prevErr, blocked := deployFailures[taskKey]; blocked {
			slog.Info("explorer: skipping task (prior deploy failure in this plan)",
				"kind", task.Kind, "model", task.Model, "engine", task.Engine,
				"prior_error", prevErr)
			task.Status = "skipped"
			// Still harvest so the skip is recorded as knowledge
			skipResult := HarvestResult{Success: false, Error: fmt.Sprintf("skipped: prior deploy failure — %s", prevErr)}
			if len(task.Params) > 0 {
				skipResult.Config = make(map[string]any, len(task.Params))
				for k, v := range task.Params {
					skipResult.Config[k] = v
				}
			}
			actions := harvester.Harvest(ctx, HarvestInput{Task: *task, Result: skipResult})
			for _, a := range actions {
				slog.Info("explorer: harvest action", "type", a.Type, "detail", a.Detail)
			}
			continue
		}

		slog.Info("explorer: executing task",
			"kind", task.Kind, "model", task.Model, "engine", task.Engine,
			"progress", fmt.Sprintf("%d/%d", i+1, len(plan.Tasks)))

		taskStart := time.Now()
		result := e.executeTask(ctx, *task, plan.ID)
		taskElapsed := time.Since(taskStart)

		// Log task duration — especially valuable for tune tasks where each
		// iteration incurs a full cold-start (kill → deploy → health check).
		slog.Info("explorer: task finished",
			"kind", task.Kind, "model", task.Model, "engine", task.Engine,
			"success", result.Success, "elapsed", taskElapsed,
			"throughput", result.Throughput)

		if result.Success {
			task.Status = "completed"
		} else {
			task.Status = "failed"
			// Track deploy-level failures for intra-plan feedback
			errClass := classifyError(result.Error)
			if errClass == "deploy_crash" || errClass == "OOM" || errClass == "timeout" {
				deployFailures[taskKey] = result.Error
			}
		}

		// Harvest results
		actions := harvester.Harvest(ctx, HarvestInput{Task: *task, Result: result})
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

	// Mark plan completed (with updated task statuses in JSON)
	if e.db != nil {
		now := time.Now()
		summaryJSON := ""
		if planJSON, err := json.Marshal(plan); err == nil {
			summaryJSON = string(planJSON)
		}
		_ = e.db.UpdateExplorationPlan(ctx, &state.ExplorationPlanRow{
			ID:          plan.ID,
			Status:      "completed",
			Progress:    len(plan.Tasks),
			CompletedAt: &now,
			SummaryJSON: summaryJSON,
		})
	}
}

// defaultBenchmarkProfile returns sensible benchmark parameters based on hardware capability.
// D6: Explorer decides "how to test" (tactical), Planner decides "what to test" (strategic).
func defaultBenchmarkProfile(hw HardwareInfo) ExplorationBenchmarkProfile {
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if totalVRAM == 0 {
		totalVRAM = hw.VRAMMiB
	}
	switch {
	case totalVRAM >= 40000:
		return ExplorationBenchmarkProfile{Concurrency: 4, Rounds: 2}
	case totalVRAM >= 16000:
		return ExplorationBenchmarkProfile{Concurrency: 2, Rounds: 2}
	default:
		return ExplorationBenchmarkProfile{Concurrency: 1, Rounds: 1}
	}
}

func (e *Explorer) executeTask(ctx context.Context, task PlanTask, planID string) HarvestResult {
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
		Kind:      task.Kind,
		PlanID:    planID, // D3: plan-to-run traceability
		SourceRef: task.SourceRef,
		Target: ExplorationTarget{
			Hardware: task.Hardware,
			GPUArch:  e.cachedGPUArch,
			Model:    task.Model,
			Engine:   task.Engine,
		},
	}
	if planID != "" {
		req.Goal = fmt.Sprintf("[plan:%s] %s %s on %s", planID, task.Kind, task.Model, task.Engine)
	} else {
		req.Goal = fmt.Sprintf("%s %s on %s", task.Kind, task.Model, task.Engine)
	}
	if searchSpace != nil {
		req.SearchSpace = searchSpace
	}

	// D6: set benchmark profile from hardware defaults
	if e.gatherHardware != nil {
		if hw, err := e.gatherHardware(ctx); err == nil {
			req.Benchmark = defaultBenchmarkProfile(hw)
		}
	}

	status, err := e.explMgr.StartAndWait(ctx, req)
	if err != nil {
		return HarvestResult{Success: false, Error: err.Error()}
	}

	if status.Run.Status == "failed" {
		return HarvestResult{Success: false, Error: status.Run.Error}
	}

	// Parse benchmark results from exploration summary
	result := e.parseExplorationResult(status)
	if len(task.Params) > 0 {
		result.Config = make(map[string]any, len(task.Params))
		for key, value := range task.Params {
			result.Config[key] = value
		}
	}
	return result
}

func (e *Explorer) parseExplorationResult(status *ExplorationStatus) HarvestResult {
	result := HarvestResult{Success: true}
	// Parse summary JSON for throughput/latency data
	if status.Run.SummaryJSON != "" {
		var summary map[string]any
		if err := json.Unmarshal([]byte(status.Run.SummaryJSON), &summary); err == nil {
			readBenchmarkMetrics(summary, &result)
			if nested, ok := summary["result"].(map[string]any); ok {
				readBenchmarkMetrics(nested, &result)
			}
			if promoted, ok := summary["auto_promoted"].(bool); ok {
				result.Promoted = promoted
			}
		}
	}
	return result
}

func (e *Explorer) buildPlanInput(ctx context.Context, ev *ExplorerEvent) (*PlanInput, error) {
	input := &PlanInput{Event: ev}

	if e.gatherHardware != nil {
		hardware, err := e.gatherHardware(ctx)
		if err == nil {
			input.Hardware = hardware
			if hardware.GPUArch != "" {
				e.cachedGPUArch = hardware.GPUArch
			}
		}
	}

		// Gather gaps via the consolidated knowledge analytics path.
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

	if e.gatherOpenQuestions != nil {
		openQuestions, err := e.gatherOpenQuestions(ctx)
		if err == nil {
			input.OpenQuestions = openQuestions
		}
	}

	if e.gatherAdvisories != nil {
		advisories, err := e.gatherAdvisories(ctx)
		if err == nil {
			input.Advisories = advisories
		}
	}

	if e.gatherLocalModels != nil {
		models, err := e.gatherLocalModels(ctx)
		if err == nil {
			input.LocalModels = models
		}
	}

	if e.gatherLocalEngines != nil {
		engines, err := e.gatherLocalEngines(ctx)
		if err == nil {
			input.LocalEngines = engines
		}
	}

	// Recent exploration history
	if e.db != nil {
		runs, _ := e.db.ListExplorationRuns(ctx, "", 10)
		for _, r := range runs {
			input.History = append(input.History, *r)
		}

		// Prefill dedup: feed all explored combos to LLM so it avoids
		// proposing already-tested tasks (cheap prefill vs expensive decode).
		combos, _ := e.db.ListExploredCombos(ctx)
		for _, c := range combos {
			if c.Completed {
				input.SkipCombos = append(input.SkipCombos, SkipCombo{
					Model: c.Model, Engine: c.Engine, Reason: "completed",
				})
			} else if c.FailCount >= maxExplorationFailures {
				input.SkipCombos = append(input.SkipCombos, SkipCombo{
					Model:  c.Model,
					Engine: c.Engine,
					Reason: fmt.Sprintf("failed:%d", c.FailCount),
				})
			}
		}
	}

	return input, nil
}


func (e *Explorer) computePlanMetrics(plan *ExplorerPlan, elapsed time.Duration, tokensUsed int) *PlanMetrics {
	m := &PlanMetrics{
		TotalTasks: len(plan.Tasks),
		DurationS:  elapsed.Seconds(),
		TokensUsed: tokensUsed,
	}
	for _, t := range plan.Tasks {
		switch {
		case t.Status == "completed":
			m.Completed++
			m.DiscoveryCount++
		case t.Status == "failed":
			m.Failed++
		case strings.HasPrefix(t.Status, "skipped") || t.Status == "":
			m.Skipped++
		}
	}
	if m.TotalTasks > 0 {
		m.SuccessRate = float64(m.Completed) / float64(m.TotalTasks)
	}
	executed := m.Completed + m.Failed
	if executed > 0 {
		m.AvgTaskDurationS = elapsed.Seconds() / float64(executed)
	}
	return m
}


func (e *Explorer) refreshTier(ctx context.Context) bool {
	// O4: If agent is available but tool mode is still unknown, probe it.
	// This resolves the Tier 1→2 self-upgrade deadlock: LLMPlanner calls
	// llm.ChatCompletion directly (not Agent.Ask), so tool mode detection
	// that normally happens inside Ask() never triggers.
	if e.agent != nil && e.agent.Available() && e.agent.ToolMode() == "unknown" {
		e.agent.ProbeToolMode(ctx)
	}

	newTier := e.detectTier()
	e.mu.Lock()
	defer e.mu.Unlock()
	if newTier == e.tier {
		return false
	}
	e.tier = newTier
	e.setupPlannerLocked()
	e.harvester = e.buildHarvesterLocked()
	return true
}

func (e *Explorer) currentHardware(ctx context.Context) (HardwareInfo, error) {
	if e.gatherHardware == nil {
		return HardwareInfo{}, fmt.Errorf("hardware gatherer not configured")
	}
	return e.gatherHardware(ctx)
}

func (e *Explorer) UpdateConfig(key, value string) (string, error) {
	e.mu.Lock()
	switch key {
	case "gap_scan_interval":
		duration, err := time.ParseDuration(value)
		if err != nil {
			e.mu.Unlock()
			return "", fmt.Errorf("parse gap_scan_interval: %w", err)
		}
		e.config.Schedule.GapScanInterval = duration
	case "sync_interval":
		duration, err := time.ParseDuration(value)
		if err != nil {
			e.mu.Unlock()
			return "", fmt.Errorf("parse sync_interval: %w", err)
		}
		e.config.Schedule.SyncInterval = duration
	case "full_audit_interval":
		duration, err := time.ParseDuration(value)
		if err != nil {
			e.mu.Unlock()
			return "", fmt.Errorf("parse full_audit_interval: %w", err)
		}
		e.config.Schedule.FullAuditInterval = duration
	case "quiet_start":
		hour, err := strconv.Atoi(value)
		if err != nil || hour < 0 || hour > 23 {
			e.mu.Unlock()
			return "", fmt.Errorf("quiet_start must be an integer between 0 and 23")
		}
		e.config.Schedule.QuietStart = hour
	case "quiet_end":
		hour, err := strconv.Atoi(value)
		if err != nil || hour < 0 || hour > 23 {
			e.mu.Unlock()
			return "", fmt.Errorf("quiet_end must be an integer between 0 and 23")
		}
		e.config.Schedule.QuietEnd = hour
	case "max_concurrent_runs":
		maxRuns, err := strconv.Atoi(value)
		if err != nil || maxRuns <= 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("max_concurrent_runs must be a positive integer")
		}
		e.config.Schedule.MaxConcurrentRuns = maxRuns
	case "enabled":
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			e.mu.Unlock()
			return "", fmt.Errorf("parse enabled: %w", err)
		}
		e.config.Enabled = enabled
	case "mode":
		switch value {
		case "continuous", "once", "budget":
			e.config.Mode = value
		default:
			e.mu.Unlock()
			return "", fmt.Errorf("mode must be continuous, once, or budget")
		}
	case "max_rounds":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("max_rounds must be a non-negative integer")
		}
		e.config.MaxRounds = n
	case "max_plan_duration":
		duration, err := time.ParseDuration(value)
		if err != nil {
			e.mu.Unlock()
			return "", fmt.Errorf("parse max_plan_duration: %w", err)
		}
		e.config.MaxPlanDuration = duration
	case "max_tokens_per_day":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("max_tokens_per_day must be a non-negative integer")
		}
		e.config.MaxTokensPerDay = n
	case "rounds_used":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("rounds_used must be a non-negative integer")
		}
		e.roundsUsed = n
	default:
		e.mu.Unlock()
		return "", fmt.Errorf("unknown explorer config key %q", key)
	}

	e.config.Schedule = normalizeScheduleConfig(e.config.Schedule)
	schedule := e.config.Schedule
	normalized := e.configValueLocked(key)
	e.mu.Unlock()

	e.scheduler.SetConfig(schedule)
	return normalized, nil
}

func (e *Explorer) configValueLocked(key string) string {
	switch key {
	case "gap_scan_interval":
		return e.config.Schedule.GapScanInterval.String()
	case "sync_interval":
		return e.config.Schedule.SyncInterval.String()
	case "full_audit_interval":
		return e.config.Schedule.FullAuditInterval.String()
	case "quiet_start":
		return strconv.Itoa(e.config.Schedule.QuietStart)
	case "quiet_end":
		return strconv.Itoa(e.config.Schedule.QuietEnd)
	case "max_concurrent_runs":
		return strconv.Itoa(e.config.Schedule.MaxConcurrentRuns)
	case "enabled":
		return strconv.FormatBool(e.config.Enabled)
	case "mode":
		return e.config.Mode
	case "max_rounds":
		return strconv.Itoa(e.config.MaxRounds)
	case "max_plan_duration":
		return e.config.MaxPlanDuration.String()
	case "max_tokens_per_day":
		return strconv.Itoa(e.config.MaxTokensPerDay)
	case "rounds_used":
		return strconv.Itoa(e.roundsUsed)
	case "tokens_used_today":
		return strconv.Itoa(e.tokensUsedToday)
	default:
		return ""
	}
}

type advisoryTask struct {
	ID   string
	Type string
}

func parseAdvisoryTask(payload json.RawMessage, defaultHardware string) (advisoryTask, PlanTask, error) {
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return advisoryTask{}, PlanTask{}, err
	}

	config := extractAdvisoryConfig(raw)
	model := firstNonEmptyJSON(raw, "target_model", "model")
	engine := firstNonEmptyJSON(raw, "target_engine", "engine")
	hardware := firstTaskHardware(firstNonEmptyJSON(raw, "target_hardware", "hardware"), defaultHardware)
	id := firstNonEmptyJSON(raw, "id")
	task := PlanTask{
		Kind:      "validate",
		Hardware:  hardware,
		Model:     model,
		Engine:    engine,
		SourceRef: id,
		Params:    config,
		Reason:    fmt.Sprintf("validate central advisory %s", id),
	}
	if task.Model == "" {
		return advisoryTask{}, PlanTask{}, fmt.Errorf("advisory missing target model")
	}
	return advisoryTask{
		ID:   id,
		Type: firstNonEmptyJSON(raw, "type"),
	}, task, nil
}

func extractAdvisoryConfig(payload map[string]any) map[string]any {
	for _, key := range []string{"config", "recommended_config", "content_json", "content", "params"} {
		value, ok := payload[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			return typed
		case string:
			var parsed map[string]any
			if err := json.Unmarshal([]byte(typed), &parsed); err == nil {
				return parsed
			}
		}
	}
	return nil
}

func firstNonEmptyJSON(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := payload[key]
		if !ok {
			continue
		}
		if str, ok := value.(string); ok && str != "" {
			return str
		}
	}
	return ""
}

func readBenchmarkMetrics(summary map[string]any, result *HarvestResult) {
	if tp, ok := summary["throughput_tps"].(float64); ok {
		result.Throughput = tp
	}
	if ttft, ok := summary["ttft_p95_ms"].(float64); ok {
		result.TTFTP95 = ttft
	}
	if vram, ok := summary["vram_usage_mib"].(float64); ok {
		result.VRAMMiB = vram
	}
}
