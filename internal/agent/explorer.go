package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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
	Mode            string // "continuous" | "once" | "budget"
	MaxRounds       int    // budget mode: max plans to execute (0=unlimited)
	MaxTokensPerDay int    // daily LLM token cap (0=unlimited)
	MaxCycles       int    // PDCA max iterations per round (default 3)
	MaxTasks        int    // max tasks per plan (default 5)
	WorkspaceDir    string // workspace root (default ~/.aima/explorer/)
}

// ExplorerStatus reports the Explorer's current state.
type ExplorerStatus struct {
	Running          bool           `json:"running"`
	Enabled          bool           `json:"enabled"`
	Phase            string         `json:"phase"`
	Tier             int            `json:"tier"`
	ActivePlan       *ExplorerPlan  `json:"active_plan,omitempty"`
	Schedule         ScheduleConfig `json:"schedule"`
	LastRun          time.Time      `json:"last_run,omitempty"`
	Mode             string         `json:"mode"`
	RoundsUsed       int            `json:"rounds_used"`
	MaxRounds        int            `json:"max_rounds"`
	TokensUsedToday  int            `json:"tokens_used_today"`
	MaxTokensPerDay  int            `json:"max_tokens_per_day"`
	MaxCycles        int            `json:"max_cycles"`
	MaxTasks         int            `json:"max_tasks"`
	LastPlanMetrics  *PlanMetrics   `json:"last_plan_metrics,omitempty"`
	BlockedByDeploys []DeployStatus `json:"blocked_by_deploys,omitempty"`
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
const recentExplorationHistoryLimit = 30

// Explorer orchestrates autonomous knowledge discovery on edge devices.
type Explorer struct {
	config    ExplorerConfig
	agent     *Agent
	explMgr   *ExplorationManager
	db        *state.DB
	bus       *EventBus
	scheduler *Scheduler
	planner   Planner
	workspace *ExplorerWorkspace // PDCA document workspace
	harvester *Harvester

	// Data gathering functions, wired via options or buildToolDeps.
	gatherHardware      func(ctx context.Context) (HardwareInfo, error)
	gatherGaps          func(ctx context.Context) ([]GapEntry, error)
	gatherDeploys       func(ctx context.Context) ([]DeployStatus, error)
	gatherOpenQuestions func(ctx context.Context) ([]OpenQuestion, error)
	gatherAdvisories    func(ctx context.Context) ([]Advisory, error)
	gatherLocalModels   func(ctx context.Context) ([]LocalModel, error)
	gatherLocalEngines  func(ctx context.Context) ([]LocalEngine, error)
	gatherComboFacts    func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error)

	// Harvester callbacks, wired via options or buildToolDeps.
	syncPush func(ctx context.Context) error
	saveNote func(ctx context.Context, title, content, hardware, model, engine string) error

	// Pre-cycle cleanup: stop all existing deployments to free GPU memory.
	cleanupDeploys func(ctx context.Context) (int, error)

	// Advisory feedback callback, wired via WithAdvisoryFeedback.
	advisoryFeedback func(ctx context.Context, advisoryID, status, reason string) error

	// Knowledge query function, wired via WithExplorerQueryFunc for agent planner.
	queryFn QueryFunc

	// Benchmark profile resolver from catalog YAML, wired via WithBenchmarkProfiles.
	benchmarkProfilesFn func(totalVRAMMiB int) []ExplorationBenchmarkProfile

	mu            sync.RWMutex
	running       bool
	tier          int
	phase         string
	activePlan    *ExplorerPlan
	cachedGPUArch string // cached from gatherHardware for overlay YAML (O13)
	lastRun       time.Time
	cancel        context.CancelFunc

	// T2: Resource control state
	roundsUsed      int
	tokensUsedToday int
	tokenResetDate  string // "2006-01-02"

	// T5: Plan metrics
	lastPlanMetrics *PlanMetrics

	// Pre-cycle block: set when active deployments detected, cleared by CleanupDeploys.
	blockedByDeploys []DeployStatus
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

// WithGatherComboFacts sets the function to compute authoritative ready/blocked combo facts.
func WithGatherComboFacts(fn func(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine) ([]ComboFact, error)) ExplorerOption {
	return func(e *Explorer) { e.gatherComboFacts = fn }
}

// WithCleanupDeploys sets the function to stop all existing deployments before
// an exploration cycle. Returns the number of deployments cleaned up.
func WithCleanupDeploys(fn func(ctx context.Context) (int, error)) ExplorerOption {
	return func(e *Explorer) { e.cleanupDeploys = fn }
}

// WithAdvisoryFeedback sets the callback for sending advisory feedback to central.
func WithAdvisoryFeedback(fn func(ctx context.Context, advisoryID, status, reason string) error) ExplorerOption {
	return func(e *Explorer) { e.advisoryFeedback = fn }
}

// WithExplorerQueryFunc sets the knowledge base query function for agent planner.
func WithExplorerQueryFunc(fn QueryFunc) ExplorerOption {
	return func(e *Explorer) { e.queryFn = fn }
}

// WithBenchmarkProfiles sets the function to resolve VRAM-tiered benchmark profiles from catalog.
func WithBenchmarkProfiles(fn func(totalVRAMMiB int) []ExplorationBenchmarkProfile) ExplorerOption {
	return func(e *Explorer) { e.benchmarkProfilesFn = fn }
}

// WithRoundsUsed restores the rounds counter from persisted state on restart.
func WithRoundsUsed(n int) ExplorerOption {
	return func(e *Explorer) { e.roundsUsed = n }
}

func NewExplorer(config ExplorerConfig, agent *Agent, explMgr *ExplorationManager, db *state.DB, bus *EventBus, opts ...ExplorerOption) *Explorer {
	if config.MaxCycles <= 0 {
		config.MaxCycles = 3
	}
	if config.MaxTasks <= 0 {
		config.MaxTasks = 5
	}
	e := &Explorer{
		config:  config,
		agent:   agent,
		explMgr: explMgr,
		db:      db,
		bus:     bus,
		phase:   "idle",
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

// persistConfigKey writes a single explorer config key to the database.
// Key names match loadExplorerConfig in cmd/aima/main.go (e.g. "enabled", "rounds_used").
func (e *Explorer) persistConfigKey(ctx context.Context, key, value string) {
	if e.db == nil {
		return
	}
	if err := e.db.SetConfig(ctx, "explorer."+key, value); err != nil {
		slog.Warn("explorer: persist config failed", "key", key, "error", err)
	}
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
		wsDir := e.config.WorkspaceDir
		if wsDir == "" {
			home, _ := os.UserHomeDir()
			wsDir = filepath.Join(home, ".aima", "explorer")
		}
		e.workspace = NewExplorerWorkspace(wsDir)
		opts := []ExplorerAgentPlannerOption{
			WithAgentMaxCycles(e.config.MaxCycles),
			WithAgentMaxTasks(e.config.MaxTasks),
		}
		if e.queryFn != nil {
			opts = append(opts, WithAgentQueryFunc(e.queryFn))
		}
		e.planner = NewExplorerAgentPlanner(e.agent.llm, e.workspace, opts...)
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
	e.phase = "idle"
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running = false
		e.activePlan = nil
		e.phase = "stopped"
		e.mu.Unlock()
	}()

	if e.isEnabled() {
		slog.Info("explorer started", "tier", e.tier)
	} else {
		slog.Info("explorer started in disabled mode", "tier", e.tier)
	}

	e.reconcileStaleExplorationPlans(ctx)

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
			// D5: After handling an event (which may include multi-minute plan
			// execution), drain stale events that accumulated during processing.
			// This prevents re-processing 14 gap_scan events that piled up
			// during an 8-minute PDCA cycle.
			drained := 0
			for {
				select {
				case stale := <-ch:
					drained++
					slog.Debug("explorer: drained stale event", "type", stale.Type)
				default:
					goto drainDone
				}
			}
		drainDone:
			if drained > 0 {
				slog.Info("explorer: drained stale events after plan execution", "count", drained)
			}
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
	e.phase = "stopped"
}

// Status returns the Explorer's current state.
func (e *Explorer) Status() ExplorerStatus {
	e.refreshTier(context.Background())
	if _, err := e.refreshBlockedDeploys(context.Background()); err != nil {
		slog.Debug("explorer: status blocked deploy refresh failed", "error", err)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return ExplorerStatus{
		Running:          e.running,
		Enabled:          e.config.Enabled,
		Phase:            e.phase,
		Tier:             e.tier,
		ActivePlan:       e.activePlan,
		Schedule:         e.config.Schedule,
		LastRun:          e.lastRun,
		Mode:             e.config.Mode,
		RoundsUsed:       e.roundsUsed,
		MaxRounds:        e.config.MaxRounds,
		TokensUsedToday:  e.tokensUsedToday,
		MaxTokensPerDay:  e.config.MaxTokensPerDay,
		MaxCycles:        e.config.MaxCycles,
		MaxTasks:         e.config.MaxTasks,
		LastPlanMetrics:  e.lastPlanMetrics,
		BlockedByDeploys: e.blockedByDeploys,
	}
}

func (e *Explorer) setPhase(phase string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.phase = phase
}

func (e *Explorer) refreshBlockedDeploys(ctx context.Context) ([]DeployStatus, error) {
	if e.gatherDeploys == nil {
		e.mu.Lock()
		e.blockedByDeploys = nil
		e.mu.Unlock()
		return nil, nil
	}
	deploys, err := e.gatherDeploys(ctx)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	if len(deploys) == 0 {
		e.blockedByDeploys = nil
	} else {
		e.blockedByDeploys = append([]DeployStatus(nil), deploys...)
	}
	e.mu.Unlock()
	return deploys, nil
}

// CleanupDeploys stops all active deployments to free GPU memory.
// Called explicitly by the user via explorer action=cleanup.
// Returns the number of deployments cleaned up.
func (e *Explorer) CleanupDeploys(ctx context.Context) (int, error) {
	if e.cleanupDeploys == nil {
		return 0, fmt.Errorf("cleanup not available")
	}
	cleaned, err := e.cleanupDeploys(ctx)
	if err != nil {
		return 0, err
	}
	if cleaned > 0 {
		time.Sleep(gpuReleaseGrace)
	}
	e.mu.Lock()
	e.blockedByDeploys = nil
	e.mu.Unlock()
	slog.Info("explorer: user-confirmed cleanup completed", "stopped", cleaned)
	return cleaned, nil
}

func (e *Explorer) claimPlanRound(ctx context.Context, mode string, maxRounds int) (int, bool) {
	e.mu.Lock()
	if mode == "once" && e.roundsUsed >= 1 {
		roundsUsed := e.roundsUsed
		e.mu.Unlock()
		return roundsUsed, false
	}
	if mode == "budget" && maxRounds > 0 && e.roundsUsed >= maxRounds {
		roundsUsed := e.roundsUsed
		e.mu.Unlock()
		return roundsUsed, false
	}
	e.roundsUsed++
	roundsUsed := e.roundsUsed
	e.mu.Unlock()

	e.persistConfigKey(ctx, "rounds_used", strconv.Itoa(roundsUsed))
	return roundsUsed, true
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
		e.rejectAdvisoryEvent(ctx, ev, "explorer is disabled on this device")
		return
	}

	e.reconcileStaleExplorationPlans(ctx)

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
		e.phase = "once_complete"
		e.mu.Unlock()
		e.persistConfigKey(ctx, "enabled", "false")
		slog.Info("explorer: once mode completed, auto-disabling")
		e.rejectAdvisoryEvent(ctx, ev, "explorer once mode already completed on this device")
		return
	}
	if mode == "budget" && maxRounds > 0 && roundsUsed >= maxRounds {
		e.setPhase("budget_exhausted")
		if _, err := e.refreshBlockedDeploys(ctx); err != nil {
			slog.Debug("explorer: refresh blocked deploys failed after budget exhaustion", "error", err)
		}
		slog.Info("explorer: budget exhausted", "rounds_used", roundsUsed, "max_rounds", maxRounds)
		e.rejectAdvisoryEvent(ctx, ev, "explorer budget exhausted on this device")
		return
	}
	if maxTokens > 0 && tokensUsed >= maxTokens {
		e.setPhase("token_budget_exhausted")
		slog.Info("explorer: daily token budget exhausted", "used", tokensUsed, "max", maxTokens)
		e.rejectAdvisoryEvent(ctx, ev, "explorer daily token budget exhausted on this device")
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

	// Pre-cycle check: block if active deployments consume GPU memory.
	// Single-model exploration needs a clean slate for accurate VRAM readings.
	// The user must explicitly call explorer action=cleanup to proceed.
	// Only gated when cleanupDeploys is wired — otherwise no action available.
	if e.cleanupDeploys != nil && e.gatherDeploys != nil {
		deploys, gErr := e.refreshBlockedDeploys(ctx)
		if gErr == nil && len(deploys) > 0 {
			names := make([]string, 0, len(deploys))
			for _, d := range deploys {
				names = append(names, d.Model+"("+d.Engine+")")
			}
			e.setPhase("blocked_by_deploys")
			slog.Warn("explorer: cycle blocked — active deployments occupy GPU memory",
				"count", len(deploys), "deployments", strings.Join(names, ", "))
			return
		}
	}

	// Build plan input from current state
	e.setPhase("planning")
	slog.Info("explorer: building plan input")
	input, err := e.buildPlanInput(ctx, &ev)
	if err != nil {
		e.setPhase("idle")
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
		e.setPhase("idle")
		if tier >= 2 {
			slog.Warn("explorer: tier 2 planner failed", "error", err)
		} else {
			slog.Warn("explorer: plan generation failed", "error", err)
		}
		// If LLM planner failed, try rule planner fallback
		if tier >= 2 {
			slog.Info("explorer: degrading to Tier 1 planner")
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
			structural, _ := e.db.HasStructuralExplorationFailure(ctx, t.Model, t.Engine)
			if structural {
				slog.Info("explorer: dedup skipped (structural failure)", "model", t.Model, "engine", t.Engine)
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
		enabled := true
		_, _ = e.claimPlanRound(ctx, mode, maxRounds)
		e.mu.Lock()
		if mode == "once" {
			e.config.Enabled = false
			enabled = false
		}
		e.mu.Unlock()
		if !enabled {
			e.persistConfigKey(ctx, "enabled", "false")
		}
		if mode == "once" {
			slog.Info("explorer: once mode completed, auto-disabling")
		}
		e.setPhase("idle")
		return
	}

	// Persist plan
	if e.db != nil {
		if err := e.persistExplorationPlan(ctx, plan, ev.Type); err != nil {
			slog.Warn("explorer: persist plan failed", "error", err)
		}
	}

	// D1: synchronous execution — budget, dedup, and activePlan are naturally correct
	roundsUsed, ok := e.claimPlanRound(ctx, mode, maxRounds)
	if !ok {
		if mode == "budget" && maxRounds > 0 {
			slog.Info("explorer: budget exhausted", "rounds_used", roundsUsed, "max_rounds", maxRounds)
		}
		return
	}
	e.mu.Lock()
	e.activePlan = plan
	e.lastRun = time.Now()
	e.phase = "do"
	e.mu.Unlock()

	// No hard timeout — PDCA runs until completion, budget exhaustion, or user-initiated Stop().
	// The parent ctx is already cancellable via Explorer.Stop().

	e.mu.RLock()
	tokensBefore := e.tokensUsedToday
	e.mu.RUnlock()

	planStart := time.Now()
	e.executePlan(ctx, plan)

	// PDCA Check+Act loop (only for AnalyzablePlanner, i.e., Tier 2 agent planner)
	maxCycles := e.config.MaxCycles
	if maxCycles <= 0 {
		maxCycles = 3
	}
	if ap, ok := e.planner.(AnalyzablePlanner); ok {
		for cycle := 0; cycle < maxCycles; cycle++ {
			select {
			case <-ctx.Done():
				slog.Info("explorer: PDCA cancelled by user", "cycle", cycle)
				goto pdcaDone
			default:
			}

			if refresher, ok := ap.(FactRefreshablePlanner); ok {
				input, err := e.buildPlanInput(ctx, &ev)
				if err != nil {
					slog.Warn("explorer: PDCA fact refresh build failed", "error", err, "cycle", cycle+1)
				} else if err := refresher.RefreshFacts(*input); err != nil {
					slog.Warn("explorer: PDCA fact refresh failed", "error", err, "cycle", cycle+1)
				}
			}

			slog.Info("explorer: PDCA Check phase", "cycle", cycle+1)
			e.setPhase("check")
			verdict, extraTasks, analyzeTokens, err := ap.Analyze(ctx)
			if analyzeTokens > 0 {
				e.mu.Lock()
				e.tokensUsedToday += analyzeTokens
				e.mu.Unlock()
			}
			if err != nil {
				slog.Warn("explorer: PDCA analyze failed", "error", err, "cycle", cycle+1)
				if errors.Is(err, context.Canceled) {
					break
				}
				slog.Info("explorer: transient analyze error, continuing to next cycle", "cycle", cycle+1)
				continue
			}
			slog.Info("explorer: PDCA verdict", "verdict", verdict, "extra_tasks", len(extraTasks), "cycle", cycle+1)

			if verdict != "continue" || len(extraTasks) == 0 {
				break
			}

			extraPlanTasks := make([]PlanTask, len(extraTasks))
			for i, ts := range extraTasks {
				extraPlanTasks[i] = taskSpecToPlanTask(ts, plan.Tasks[0].Hardware)
			}
			extraPlan := &ExplorerPlan{
				ID:        plan.ID + fmt.Sprintf("-c%d", cycle+1),
				Tier:      2,
				Tasks:     extraPlanTasks,
				Reasoning: fmt.Sprintf("PDCA Act cycle %d", cycle+1),
			}
			roundsUsed, ok := e.claimPlanRound(ctx, mode, maxRounds)
			if !ok {
				if mode == "budget" && maxRounds > 0 {
					slog.Info("explorer: budget exhausted", "rounds_used", roundsUsed, "max_rounds", maxRounds)
				}
				break
			}
			slog.Info("explorer: PDCA Do phase", "tasks", len(extraPlanTasks), "cycle", cycle+1)
			if e.db != nil {
				if err := e.persistExplorationPlan(ctx, extraPlan, fmt.Sprintf("%s:pdca-act-%d", ev.Type, cycle+1)); err != nil {
					slog.Warn("explorer: persist PDCA plan failed", "error", err, "cycle", cycle+1)
				}
			}
			e.mu.Lock()
			e.activePlan = extraPlan
			e.phase = "do"
			e.mu.Unlock()
			e.executePlan(ctx, extraPlan)
		}
	}
pdcaDone:
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
	e.phase = "idle"
	if mode == "once" {
		e.config.Enabled = false
		e.phase = "once_complete"
	}
	e.mu.Unlock()
	if mode == "once" {
		e.persistConfigKey(ctx, "enabled", "false")
		slog.Info("explorer: once mode completed, auto-disabling")
	}

	// D10: log what's still deployed when budget exhausted
	if mode == "budget" && maxRounds > 0 && e.roundsUsed >= maxRounds {
		e.setPhase("budget_exhausted")
		if deploys, err := e.refreshBlockedDeploys(ctx); err == nil && len(deploys) > 0 {
			e.setPhase("blocked_by_deploys")
			names := make([]string, 0, len(deploys))
			for _, d := range deploys {
				names = append(names, d.Model+"("+d.Engine+")")
			}
			slog.Warn("explorer: budget exhausted with active deployments still running",
				"count", len(deploys), "deployments", strings.Join(names, ", "))
		} else if err != nil {
			slog.Debug("explorer: post-cycle deploy refresh failed", "error", err)
		}
		slog.Info("explorer: budget exhausted")
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
		e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", "no exploration capability on this device")
		return
	}

	go func() {
		e.executeAdvisoryValidation(ctx, advisory, task)
	}()
}

func (e *Explorer) rejectAdvisoryEvent(ctx context.Context, ev ExplorerEvent, reason string) {
	if ev.Type != EventCentralAdvisory || len(ev.Advisory) == 0 {
		return
	}
	advisory, _, err := parseAdvisoryTask(ev.Advisory, "")
	if err != nil {
		slog.Debug("explorer: advisory rejection parse skipped", "error", err)
		return
	}
	e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", reason)
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
	switch status {
	case "accepted", "validated", "rejected":
	default:
		slog.Warn("explorer: unsupported advisory feedback status, normalizing to rejected",
			"advisory_id", advisoryID, "status", status)
		status = "rejected"
	}
	if err := e.advisoryFeedback(ctx, advisoryID, status, reason); err != nil {
		slog.Warn("explorer: advisory feedback failed",
			"advisory_id", advisoryID, "error", err)
	}
}

func (e *Explorer) advisoryTaskAllowed(ctx context.Context, task PlanTask) (bool, string) {
	var (
		hardware HardwareInfo
		models   []LocalModel
		engines  []LocalEngine
	)
	if e.gatherHardware != nil {
		if hw, err := e.gatherHardware(ctx); err == nil {
			hardware = hw
		}
	}
	if e.gatherLocalModels != nil {
		if localModels, err := e.gatherLocalModels(ctx); err == nil {
			models = localModels
		}
	}
	if e.gatherLocalEngines != nil {
		if localEngines, err := e.gatherLocalEngines(ctx); err == nil {
			engines = localEngines
		}
	}
	if e.gatherComboFacts != nil {
		if comboFacts, err := e.gatherComboFacts(ctx, hardware, models, engines); err == nil {
			if reason, blocked := taskBlockedByExecutableFacts(task.Model, task.Engine, comboFacts, nil); blocked {
				return false, reason
			}
		}
	}
	if e.db != nil {
		completed, _ := e.db.HasCompletedExploration(ctx, task.Model, task.Engine)
		if completed {
			return false, "already completed on this device"
		}
		structural, _ := e.db.HasStructuralExplorationFailure(ctx, task.Model, task.Engine)
		if structural {
			return false, "combo already has a structural exploration failure on this device"
		}
		failCount, _ := e.db.CountFailedExplorations(ctx, task.Model, task.Engine)
		if failCount >= maxExplorationFailures {
			return false, fmt.Sprintf("combo already failed %d times on this device", failCount)
		}
	}
	return true, ""
}

func taskStatusFromHarvest(result HarvestResult) string {
	if result.Success {
		return "completed"
	}
	if result.Cancelled {
		return "cancelled"
	}
	return "failed"
}

func (e *Explorer) finalizeExplorationPlanRecord(ctx context.Context, plan *ExplorerPlan, terminalStatus string) {
	if e.db == nil || plan == nil {
		return
	}
	updateCtx := ctx
	if updateCtx == nil || updateCtx.Err() != nil {
		updateCtx = context.Background()
	}
	now := time.Now()
	summaryJSON := ""
	if planJSON, err := json.Marshal(plan); err == nil {
		summaryJSON = string(planJSON)
	}
	if err := e.db.UpdateExplorationPlan(updateCtx, &state.ExplorationPlanRow{
		ID:          plan.ID,
		Status:      terminalStatus,
		Progress:    len(plan.Tasks),
		CompletedAt: &now,
		SummaryJSON: summaryJSON,
	}); err != nil {
		slog.Debug("explorer: finalize plan failed", "error", err, "plan_id", plan.ID, "status", terminalStatus)
	}
}

func (e *Explorer) executeAdvisoryValidation(ctx context.Context, advisory advisoryTask, task PlanTask) {
	if e.cleanupDeploys != nil && e.gatherDeploys != nil {
		if deploys, err := e.refreshBlockedDeploys(ctx); err == nil && len(deploys) > 0 {
			e.setPhase("blocked_by_deploys")
			e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", "active deployments already occupy this device")
			return
		}
	}

	if ok, reason := e.advisoryTaskAllowed(ctx, task); !ok {
		slog.Info("explorer: advisory validation rejected by local execution facts",
			"id", advisory.ID, "model", task.Model, "engine", task.Engine, "reason", reason)
		e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", reason)
		return
	}

	e.mu.RLock()
	mode := e.config.Mode
	maxRounds := e.config.MaxRounds
	e.mu.RUnlock()
	if mode == "budget" && maxRounds > 0 {
		if roundsUsed, ok := e.claimPlanRound(ctx, mode, maxRounds); !ok {
			e.setPhase("budget_exhausted")
			e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", fmt.Sprintf("explorer budget exhausted on this device (%d/%d rounds)", roundsUsed, maxRounds))
			return
		}
	}

	plan := &ExplorerPlan{
		ID:        "advisory-" + firstNonEmpty(advisory.ID, strconv.FormatInt(time.Now().UnixNano(), 10)),
		Tier:      e.Status().Tier,
		Tasks:     []PlanTask{task},
		Reasoning: "central advisory validation",
	}
	if e.db != nil {
		if err := e.persistExplorationPlan(ctx, plan, string(EventCentralAdvisory)); err != nil {
			slog.Debug("explorer: persist advisory plan failed", "error", err, "plan_id", plan.ID)
		}
	}

	e.mu.Lock()
	e.activePlan = plan
	e.lastRun = time.Now()
	e.phase = "advisory"
	e.mu.Unlock()

	taskStart := time.Now()
	result := e.executeTask(ctx, task, plan.ID)
	taskElapsed := time.Since(taskStart)
	plan.Tasks[0].Status = taskStatusFromHarvest(result)

	harvester := e.currentHarvester()
	if harvester != nil {
		actions := harvester.Harvest(ctx, HarvestInput{Task: task, Result: result})
		for _, a := range actions {
			slog.Info("explorer: advisory harvest action", "type", a.Type, "detail", a.Detail)
		}
	}

	if e.workspace != nil {
		if _, err := e.workspace.WriteExperimentResult(1, taskSpecFromPlanTask(task), harvestToExperimentResult(plan.Tasks[0].Status, taskStart, taskElapsed, result)); err != nil {
			slog.Debug("explorer: write advisory experiment result failed", "error", err)
		}
	}

	terminalStatus := plan.Tasks[0].Status
	if ctx.Err() != nil {
		terminalStatus = "cancelled"
	}
	e.finalizeExplorationPlanRecord(ctx, plan, terminalStatus)

	e.mu.Lock()
	e.activePlan = nil
	e.phase = "idle"
	e.mu.Unlock()

	if e.cleanupDeploys != nil && e.gatherDeploys != nil {
		if deploys, err := e.refreshBlockedDeploys(ctx); err == nil && len(deploys) > 0 {
			e.setPhase("blocked_by_deploys")
		}
	}

	if result.Success {
		reason := fmt.Sprintf("validated: %.1f tok/s, TTFT P95 %.0fms", result.Throughput, result.TTFTP95)
		e.sendAdvisoryFeedback(ctx, advisory.ID, "accepted", reason)
		return
	}
	e.sendAdvisoryFeedback(ctx, advisory.ID, "rejected", "validation failed: "+result.Error)
}

func (e *Explorer) executePlan(ctx context.Context, plan *ExplorerPlan) {
	harvester := e.currentHarvester()
	terminalStatus := "completed"
	defer func() {
		if ctx.Err() != nil {
			terminalStatus = "cancelled"
		}
		e.finalizeExplorationPlanRecord(ctx, plan, terminalStatus)
	}()
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
				if e.workspace != nil {
					timeoutResult := HarvestResult{
						Success: false,
						Error:   "skipped: timeout before execution",
					}
					timeoutTask := taskSpecFromPlanTask(plan.Tasks[j])
					if _, werr := e.workspace.WriteExperimentResult(j+1, timeoutTask, harvestToExperimentResult(plan.Tasks[j].Status, time.Now(), 0, timeoutResult)); werr != nil {
						slog.Debug("explorer: write timeout experiment result failed", "error", werr)
					}
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
			if e.workspace != nil {
				if _, werr := e.workspace.WriteExperimentResult(i+1, taskSpecFromPlanTask(*task), harvestToExperimentResult(task.Status, time.Now(), 0, skipResult)); werr != nil {
					slog.Debug("explorer: write skipped experiment result failed", "error", werr)
				}
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
		} else if result.Cancelled {
			task.Status = "cancelled"
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

		// Write experiment result to workspace (for PDCA Check phase)
		if e.workspace != nil {
			expResult := harvestToExperimentResult(task.Status, taskStart, taskElapsed, result)
			if _, werr := e.workspace.WriteExperimentResult(i+1, taskSpecFromPlanTask(*task), expResult); werr != nil {
				slog.Debug("explorer: write experiment result failed", "error", werr)
			}
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
}

func (e *Explorer) persistExplorationPlan(ctx context.Context, plan *ExplorerPlan, trigger string) error {
	if e.db == nil || plan == nil {
		return nil
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("marshal plan %s: %w", plan.ID, err)
	}
	return e.db.InsertExplorationPlan(ctx, &state.ExplorationPlanRow{
		ID:        plan.ID,
		Tier:      plan.Tier,
		Trigger:   trigger,
		Status:    "active",
		PlanJSON:  string(planJSON),
		Total:     len(plan.Tasks),
		CreatedAt: time.Now(),
	})
}

func (e *Explorer) reconcileStaleExplorationPlans(ctx context.Context) {
	if e.db == nil {
		return
	}
	activePlanID := ""
	e.mu.RLock()
	if e.activePlan != nil {
		activePlanID = e.activePlan.ID
	}
	e.mu.RUnlock()

	plans, err := e.db.ListExplorationPlans(ctx, "active")
	if err != nil {
		slog.Debug("explorer: list active plans failed", "error", err)
		return
	}

	now := time.Now()
	for _, plan := range plans {
		if plan == nil || plan.ID == "" || plan.ID == activePlanID {
			continue
		}
		summaryJSON := plan.SummaryJSON
		if summaryJSON == "" {
			if b, err := json.Marshal(map[string]any{
				"reconciled": true,
				"reason":     "stale active plan",
			}); err == nil {
				summaryJSON = string(b)
			}
		}
		if err := e.db.UpdateExplorationPlan(context.Background(), &state.ExplorationPlanRow{
			ID:          plan.ID,
			Status:      "cancelled",
			Progress:    plan.Progress,
			CompletedAt: &now,
			SummaryJSON: summaryJSON,
		}); err != nil {
			slog.Debug("explorer: stale plan reconciliation failed", "error", err, "plan_id", plan.ID)
		}
	}
}

func extractPlanIDFromGoal(goal string) string {
	const prefix = "[plan:"
	if !strings.HasPrefix(goal, prefix) {
		return ""
	}
	end := strings.Index(goal, "]")
	if end <= len(prefix) {
		return ""
	}
	return goal[len(prefix):end]
}

func parsePlanTaskStatuses(summaryJSON string) map[string]string {
	if strings.TrimSpace(summaryJSON) == "" {
		return nil
	}
	var summary struct {
		Tasks []PlanTask
	}
	if err := json.Unmarshal([]byte(summaryJSON), &summary); err != nil {
		return nil
	}
	statuses := make(map[string]string, len(summary.Tasks))
	for _, task := range summary.Tasks {
		if task.Model == "" || task.Engine == "" || task.Status == "" {
			continue
		}
		statuses[task.Model+"|"+task.Engine] = task.Status
	}
	return statuses
}

func canonicalRunStatusFromPlanTask(taskStatus string) string {
	switch taskStatus {
	case "failed":
		return "failed"
	case "cancelled", "skipped_timeout":
		return "cancelled"
	default:
		return ""
	}
}

func (e *Explorer) reconcileHistoricalExplorationRuns(ctx context.Context) {
	if e.db == nil {
		return
	}
	plans, err := e.db.ListExplorationPlans(ctx, "")
	if err != nil || len(plans) == 0 {
		return
	}
	planTasks := make(map[string]map[string]string, len(plans))
	for _, plan := range plans {
		if plan == nil || plan.ID == "" {
			continue
		}
		if statuses := parsePlanTaskStatuses(plan.SummaryJSON); len(statuses) > 0 {
			planTasks[plan.ID] = statuses
		}
	}
	if len(planTasks) == 0 {
		return
	}
	runs, err := e.db.ListExplorationRuns(ctx, "", 200)
	if err != nil {
		return
	}
	for _, run := range runs {
		if run == nil || run.Status != "completed" {
			continue
		}
		planID := extractPlanIDFromGoal(run.Goal)
		if planID == "" {
			continue
		}
		statuses := planTasks[planID]
		if len(statuses) == 0 {
			continue
		}
		taskStatus := statuses[run.ModelID+"|"+run.EngineID]
		canonical := canonicalRunStatusFromPlanTask(taskStatus)
		if canonical == "" || canonical == run.Status {
			continue
		}
		run.Status = canonical
		if run.Error == "" {
			run.Error = fmt.Sprintf("reconciled from plan %s: task status=%s", planID, taskStatus)
		}
		if err := e.db.UpdateExplorationRun(context.Background(), run); err != nil {
			slog.Debug("explorer: reconcile historical run failed", "error", err, "run_id", run.ID, "plan_id", planID)
			continue
		}
		slog.Info("explorer: reconciled historical run status",
			"run_id", run.ID, "plan_id", planID, "model", run.ModelID, "engine", run.EngineID,
			"from", "completed", "to", canonical)
	}
}

// resolveBenchmarkProfiles returns matrix profiles from catalog YAML, falling back to Go defaults.
func (e *Explorer) resolveBenchmarkProfiles(hw HardwareInfo) []ExplorationBenchmarkProfile {
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if totalVRAM == 0 {
		totalVRAM = hw.VRAMMiB
	}
	if e.benchmarkProfilesFn != nil {
		if profiles := e.benchmarkProfilesFn(totalVRAM); len(profiles) > 0 {
			return profiles
		}
	}
	return defaultBenchmarkProfiles(hw)
}

// benchmarkSpecToProfile converts a planner-authored BenchmarkSpec into an ExplorationBenchmarkProfile.
func benchmarkSpecToProfile(spec BenchmarkSpec) ExplorationBenchmarkProfile {
	p := ExplorationBenchmarkProfile{
		Label:             "plan",
		ConcurrencyLevels: spec.Concurrency,
		InputTokenLevels:  spec.InputTokens,
		MaxTokenLevels:    spec.MaxTokens,
		RequestsPerCombo:  spec.RequestsPerCombo,
		Rounds:            1,
	}
	// If only single-value arrays, also set legacy single-point fields for tune-style tasks.
	if len(spec.Concurrency) == 1 {
		p.Concurrency = spec.Concurrency[0]
	}
	return p
}

func taskSpecFromPlanTask(task PlanTask) TaskSpec {
	return TaskSpec{
		Kind:         task.Kind,
		Model:        task.Model,
		Engine:       task.Engine,
		EngineParams: task.Params,
		Benchmark:    task.Benchmark,
		Reason:       task.Reason,
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

func defaultBenchmarkProfiles(hw HardwareInfo) []ExplorationBenchmarkProfile {
	totalVRAM := hw.VRAMMiB * hw.GPUCount
	if totalVRAM == 0 {
		totalVRAM = hw.VRAMMiB
	}

	var profiles []ExplorationBenchmarkProfile

	switch {
	case totalVRAM >= 40000:
		profiles = append(profiles, ExplorationBenchmarkProfile{
			Label:             "latency",
			ConcurrencyLevels: []int{1},
			InputTokenLevels:  []int{128, 512, 1024, 2048, 4096, 8192},
			MaxTokenLevels:    []int{256, 1024},
			RequestsPerCombo:  5,
			Rounds:            1,
		})
		profiles = append(profiles, ExplorationBenchmarkProfile{
			Label:             "throughput",
			ConcurrencyLevels: []int{1, 2, 4, 8},
			InputTokenLevels:  []int{512, 2048, 8192},
			MaxTokenLevels:    []int{1024},
			RequestsPerCombo:  5,
			Rounds:            1,
		})
	case totalVRAM >= 16000:
		profiles = append(profiles, ExplorationBenchmarkProfile{
			Label:             "latency",
			ConcurrencyLevels: []int{1},
			InputTokenLevels:  []int{128, 512, 1024, 2048, 4096, 8192},
			MaxTokenLevels:    []int{256, 1024},
			RequestsPerCombo:  5,
			Rounds:            1,
		})
		profiles = append(profiles, ExplorationBenchmarkProfile{
			Label:             "throughput",
			ConcurrencyLevels: []int{1, 2, 4},
			InputTokenLevels:  []int{512, 2048},
			MaxTokenLevels:    []int{1024},
			RequestsPerCombo:  5,
			Rounds:            1,
		})
	default:
		profiles = append(profiles, ExplorationBenchmarkProfile{
			Label:             "latency",
			ConcurrencyLevels: []int{1},
			InputTokenLevels:  []int{128, 512, 1024, 2048},
			MaxTokenLevels:    []int{256},
			RequestsPerCombo:  5,
			Rounds:            1,
		})
	}
	return profiles
}

// extractMaxModelLen reads max_model_len (vLLM) or context_length (sglang)
// from the task params set by the LLM planner.
func extractMaxModelLen(params map[string]any) int {
	for _, key := range []string{"max_model_len", "context_length", "ctx_size", "max_context_tokens"} {
		if v, ok := params[key]; ok {
			switch x := v.(type) {
			case float64:
				return int(x)
			case int:
				return x
			case int64:
				return int(x)
			}
		}
	}
	return 0
}

var benchmarkTokenLadder = []int{128, 256, 512, 1024, 2048, 4096, 8192, 16384, 32768, 65536, 131072}

// adaptBenchmarkProfiles keeps planner intent but enriches sparse plans with a
// small bounded set of extra context points after the model is confirmed up.
// It never explodes a planner-authored matrix into a full context ladder.
func adaptBenchmarkProfiles(profiles []ExplorationBenchmarkProfile, maxModelLen int) []ExplorationBenchmarkProfile {
	if maxModelLen <= 0 || len(profiles) == 0 {
		return profiles
	}

	result := make([]ExplorationBenchmarkProfile, 0, len(profiles))
	for _, p := range profiles {
		if len(p.InputTokenLevels) == 0 {
			result = append(result, p)
			continue
		}

		minOutput := minPositiveInt(p.MaxTokenLevels)
		if minOutput <= 0 {
			minOutput = 128
		}
		maxInput := maxModelLen - minOutput
		if maxInput <= 0 {
			maxInput = maxModelLen
		}
		p.InputTokenLevels = boundedInputTokenLevels(p.InputTokenLevels, maxInput)

		minInput := minPositiveInt(p.InputTokenLevels)
		maxOutput := maxModelLen - minInput
		if maxOutput <= 0 {
			maxOutput = maxModelLen
		}
		p.MaxTokenLevels = feasibleOutputTokenLevels(p.MaxTokenLevels, maxOutput)

		result = append(result, p)
	}
	return result
}

func boundedInputTokenLevels(existing []int, maxAllowed int) []int {
	filtered := filterTokenLevelsAtOrBelow(existing, maxAllowed)
	if len(filtered) == 0 {
		if fallback := bestBenchmarkTokenLevel(maxAllowed); fallback > 0 {
			return []int{fallback}
		}
		return []int{maxAllowed}
	}
	if len(filtered) >= 3 {
		return filtered
	}

	selected := append([]int{}, filtered...)
	for _, anchor := range []int{128, 512, 2048} {
		if len(selected) >= 3 {
			break
		}
		if anchor <= maxAllowed && !containsInt(selected, anchor) {
			selected = append(selected, anchor)
		}
	}
	return normalizeTokenLevels(selected)
}

func feasibleOutputTokenLevels(existing []int, maxAllowed int) []int {
	filtered := filterTokenLevelsAtOrBelow(existing, maxAllowed)
	if len(filtered) > 0 {
		return filtered
	}
	if fallback := bestBenchmarkTokenLevel(maxAllowed); fallback > 0 {
		return []int{fallback}
	}
	return []int{maxAllowed}
}

func filterTokenLevelsAtOrBelow(levels []int, maxAllowed int) []int {
	if maxAllowed <= 0 {
		return normalizeTokenLevels(levels)
	}
	filtered := make([]int, 0, len(levels))
	for _, level := range levels {
		if level > 0 && level <= maxAllowed {
			filtered = append(filtered, level)
		}
	}
	return normalizeTokenLevels(filtered)
}

func normalizeTokenLevels(levels []int) []int {
	seen := make(map[int]bool, len(levels))
	for _, level := range levels {
		if level > 0 {
			seen[level] = true
		}
	}
	if len(seen) == 0 {
		return nil
	}
	ordered := make([]int, 0, len(seen))
	for _, level := range benchmarkTokenLadder {
		if seen[level] {
			ordered = append(ordered, level)
			delete(seen, level)
		}
	}
	for _, level := range levels {
		if seen[level] {
			ordered = append(ordered, level)
			delete(seen, level)
		}
	}
	return ordered
}

func bestBenchmarkTokenLevel(maxAllowed int) int {
	best := 0
	for _, level := range benchmarkTokenLadder {
		if level > maxAllowed {
			break
		}
		best = level
	}
	return best
}

func minPositiveInt(vals []int) int {
	for _, v := range normalizeTokenLevels(vals) {
		if v > 0 {
			return v
		}
	}
	return 0
}

func minInt(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

func containsInt(vals []int, target int) bool {
	for _, value := range vals {
		if value == target {
			return true
		}
	}
	return false
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

	// Populate model metadata from local inventory for accurate overlay YAML
	if e.gatherLocalModels != nil {
		if models, err := e.gatherLocalModels(ctx); err == nil {
			for _, m := range models {
				if strings.EqualFold(m.Name, task.Model) {
					req.Target.ModelType = m.Type
					req.Target.Family = m.Family
					req.Target.ParameterCount = m.ParameterCount
					break
				}
			}
		}
	}

	// Populate internal args + health check from engine YAML (INV-1: engine behavior = YAML)
	if e.gatherLocalEngines != nil {
		if engines, err := e.gatherLocalEngines(ctx); err == nil {
			for _, eng := range engines {
				if strings.EqualFold(eng.Name, task.Engine) || strings.EqualFold(eng.Type, task.Engine) {
					req.Target.Runtime = eng.Runtime
					req.Target.InternalArgs = eng.InternalArgs
					req.Target.HealthCheckPath = eng.HealthCheckPath
					break
				}
			}
		}
	}

	// D6: set benchmark profile — prefer plan's BenchmarkSpec, fall back to hardware defaults.
	if task.Benchmark.RequestsPerCombo > 0 || len(task.Benchmark.Concurrency) > 0 {
		// LLM planner specified benchmark parameters — use them directly.
		req.BenchmarkProfiles = []ExplorationBenchmarkProfile{benchmarkSpecToProfile(task.Benchmark)}
	} else if e.gatherHardware != nil {
		if hw, err := e.gatherHardware(ctx); err == nil {
			if task.Kind == "validate" {
				req.BenchmarkProfiles = e.resolveBenchmarkProfiles(hw)
			} else {
				// Tune tasks get a single-point profile (no matrix levels)
				bp := defaultBenchmarkProfile(hw)
				req.BenchmarkProfiles = []ExplorationBenchmarkProfile{bp}
			}
		}
	}

	// Adapt benchmark profiles to effective max_model_len:
	// expand input_token_levels to cover longer contexts and
	// filter out infeasible combos where input+output > max_model_len.
	if maxModelLen := extractMaxModelLen(task.Params); maxModelLen > 0 {
		req.BenchmarkProfiles = adaptBenchmarkProfiles(req.BenchmarkProfiles, maxModelLen)
	}

	status, err := e.explMgr.StartAndWait(ctx, req)
	if err != nil {
		return HarvestResult{
			Success:   false,
			Cancelled: errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded),
			Error:     err.Error(),
		}
	}

	result := e.parseExplorationResult(status)
	if status.Run.Status == "failed" {
		result.Success = false
		result.Error = status.Run.Error
		return result
	}
	if status.Run.Status == "cancelled" {
		result.Success = false
		result.Cancelled = true
		result.Error = status.Run.Error
		return result
	}

	// Parse benchmark results from exploration summary
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
			result.BenchmarkID = firstNonEmptyJSON(summary, "benchmark_id")
			result.ConfigID = firstNonEmptyJSON(summary, "config_id")
			result.EngineVersion = firstNonEmptyJSON(summary, "engine_version")
			result.EngineImage = firstNonEmptyJSON(summary, "engine_image")
			if usage, ok := summary["resource_usage"].(map[string]any); ok {
				result.ResourceUsage = cloneAnyMap(usage)
			}
			if cfg, ok := summary["deploy_config"].(map[string]any); ok {
				result.DeployConfig = cloneAnyMap(cfg)
			}
			if promoted, ok := summary["auto_promoted"].(bool); ok {
				result.Promoted = promoted
			}
			if tc, ok := summary["total_cells"].(float64); ok {
				result.MatrixCells = int(tc)
			}
			if sc, ok := summary["success_cells"].(float64); ok {
				result.SuccessCells = int(sc)
			}
			if mp, ok := summary["matrix_profiles"]; ok {
				matrixJSON, _ := json.Marshal(map[string]any{
					"matrix_profiles": mp,
					"total_cells":     result.MatrixCells,
					"success_cells":   result.SuccessCells,
					"deploy_config":   result.DeployConfig,
				})
				result.MatrixJSON = string(matrixJSON)
			}
		}
	}
	return result
}

func (e *Explorer) buildPlanInput(ctx context.Context, ev *ExplorerEvent) (*PlanInput, error) {
	input := &PlanInput{Event: ev}

	// Self-heal historical late-write pollution before the next planning round
	// so dedup and LLM facts both see the same canonical state.
	e.reconcileHistoricalExplorationRuns(ctx)

	// D4: Run independent gathers in parallel to reduce plan input build time.
	// gatherComboFacts depends on hardware/models/engines, so it runs after.
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherHardware != nil {
			hardware, err := e.gatherHardware(ctx)
			if err == nil {
				input.Hardware = hardware
				if hardware.GPUArch != "" {
					e.cachedGPUArch = hardware.GPUArch
				}
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherGaps != nil {
			gaps, err := e.gatherGaps(ctx)
			if err == nil {
				input.Gaps = gaps
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherDeploys != nil {
			deploys, err := e.gatherDeploys(ctx)
			if err == nil {
				input.ActiveDeploys = deploys
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherOpenQuestions != nil {
			openQuestions, err := e.gatherOpenQuestions(ctx)
			if err == nil {
				input.OpenQuestions = openQuestions
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherAdvisories != nil {
			advisories, err := e.gatherAdvisories(ctx)
			if err == nil {
				input.Advisories = advisories
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherLocalModels != nil {
			models, err := e.gatherLocalModels(ctx)
			if err == nil {
				input.LocalModels = models
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		if e.gatherLocalEngines != nil {
			engines, err := e.gatherLocalEngines(ctx)
			if err == nil {
				input.LocalEngines = engines
			}
		}
	}()

	wg.Wait()

	// gatherComboFacts depends on hardware, models, and engines gathered above.
	if e.gatherComboFacts != nil {
		comboFacts, err := e.gatherComboFacts(ctx, input.Hardware, input.LocalModels, input.LocalEngines)
		if err == nil {
			input.ComboFacts = comboFacts
		}
	}

	// Recent exploration history
	if e.db != nil {
		runs, _ := e.db.ListExplorationRuns(ctx, "", recentExplorationHistoryLimit)
		for _, r := range runs {
			input.History = append(input.History, *r)
		}

		skipReasons := make(map[string]string)
		recordSkip := func(model, engine, reason string) {
			key := planTaskComboKey(model, engine)
			if key == "" || strings.TrimSpace(reason) == "" {
				return
			}
			if _, exists := skipReasons[key]; exists {
				return
			}
			skipReasons[key] = reason
		}

		// Recent history should immediately remove exact combos from the ready
		// frontier for the rest of this run, even before they become permanent.
		for _, r := range input.History {
			switch r.Status {
			case "completed":
				recordSkip(r.ModelID, r.EngineID, "recently completed")
			case "failed":
				recordSkip(r.ModelID, r.EngineID, "recently failed")
			case "cancelled":
				recordSkip(r.ModelID, r.EngineID, "recently cancelled")
			}
		}

		// Prefill dedup: feed all explored combos to LLM so it avoids
		// proposing already-tested tasks (cheap prefill vs expensive decode).
		combos, _ := e.db.ListExploredCombos(ctx)
		for _, c := range combos {
			if c.Completed {
				recordSkip(c.Model, c.Engine, "completed")
				continue
			}
			structural, _ := e.db.HasStructuralExplorationFailure(ctx, c.Model, c.Engine)
			if structural {
				recordSkip(c.Model, c.Engine, "structural_failure")
			} else if c.FailCount >= maxExplorationFailures {
				recordSkip(c.Model, c.Engine, fmt.Sprintf("failed:%d", c.FailCount))
			}
		}
		for key, reason := range skipReasons {
			parts := strings.SplitN(key, "|", 2)
			if len(parts) != 2 {
				continue
			}
			input.SkipCombos = append(input.SkipCombos, SkipCombo{
				Model:  parts[0],
				Engine: parts[1],
				Reason: reason,
			})
		}
		sort.Slice(input.SkipCombos, func(i, j int) bool {
			if input.SkipCombos[i].Model != input.SkipCombos[j].Model {
				return strings.ToLower(input.SkipCombos[i].Model) < strings.ToLower(input.SkipCombos[j].Model)
			}
			if input.SkipCombos[i].Engine != input.SkipCombos[j].Engine {
				return strings.ToLower(input.SkipCombos[i].Engine) < strings.ToLower(input.SkipCombos[j].Engine)
			}
			return input.SkipCombos[i].Reason < input.SkipCombos[j].Reason
		})
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
		case t.Status == "cancelled":
			m.Skipped++
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
	// This resolves the Tier 1→2 self-upgrade deadlock: ExplorerAgentPlanner
	// calls llm.ChatCompletion directly (not Agent.Ask), so tool mode detection
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
	case "max_tokens_per_day":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("max_tokens_per_day must be a non-negative integer")
		}
		e.config.MaxTokensPerDay = n
	case "max_cycles":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("max_cycles must be a positive integer")
		}
		e.config.MaxCycles = n
	case "max_tasks":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			e.mu.Unlock()
			return "", fmt.Errorf("max_tasks must be a positive integer")
		}
		e.config.MaxTasks = n
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
	case "max_tokens_per_day":
		return strconv.Itoa(e.config.MaxTokensPerDay)
	case "max_cycles":
		return strconv.Itoa(e.config.MaxCycles)
	case "max_tasks":
		return strconv.Itoa(e.config.MaxTasks)
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
			return sanitizeAdvisoryConfig(typed)
		case string:
			var parsed map[string]any
			if err := json.Unmarshal([]byte(typed), &parsed); err == nil {
				return sanitizeAdvisoryConfig(parsed)
			}
		}
	}
	return nil
}

func sanitizeAdvisoryConfig(config map[string]any) map[string]any {
	if len(config) == 0 {
		return nil
	}
	sanitized := make(map[string]any, len(config))
	for key, value := range config {
		if advisoryScalarValue(value) {
			sanitized[key] = value
		}
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func advisoryScalarValue(value any) bool {
	switch value.(type) {
	case string, bool,
		float64, float32,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		json.Number:
		return true
	default:
		return false
	}
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

func harvestToExperimentResult(status string, start time.Time, elapsed time.Duration, r HarvestResult) ExperimentResult {
	exp := ExperimentResult{
		Status:        status,
		StartedAt:     start.UTC().Format(time.RFC3339),
		DurationS:     elapsed.Seconds(),
		BenchmarkID:   r.BenchmarkID,
		ConfigID:      r.ConfigID,
		EngineVersion: r.EngineVersion,
		EngineImage:   r.EngineImage,
		ResourceUsage: cloneAnyMap(r.ResourceUsage),
		DeployConfig:  cloneAnyMap(r.DeployConfig),
		MatrixCells:   r.MatrixCells,
		SuccessCells:  r.SuccessCells,
	}
	if !r.Success {
		exp.Error = r.Error
	}
	if entries := benchmarkEntriesFromMatrixJSON(r.MatrixJSON); len(entries) > 0 {
		exp.Benchmarks = entries
		return exp
	}
	if r.Throughput > 0 || r.Concurrency > 0 || r.BenchmarkID != "" {
		exp.Benchmarks = []BenchmarkEntry{{
			Concurrency:   r.Concurrency,
			InputTokens:   r.InputTokens,
			MaxTokens:     r.MaxTokens,
			ThroughputTPS: r.Throughput,
			TTFTP95Ms:     r.TTFTP95,
			TPOTP95Ms:     r.TPOTP95,
			BenchmarkID:   r.BenchmarkID,
			ConfigID:      r.ConfigID,
			EngineVersion: r.EngineVersion,
			EngineImage:   r.EngineImage,
			ResourceUsage: cloneAnyMap(r.ResourceUsage),
			Error:         r.Error,
		}}
	}
	return exp
}

func benchmarkEntriesFromMatrixJSON(matrixJSON string) []BenchmarkEntry {
	if strings.TrimSpace(matrixJSON) == "" {
		return nil
	}
	var payload struct {
		MatrixProfiles []struct {
			Label string `json:"label"`
			Cells []struct {
				Concurrency   int            `json:"concurrency"`
				InputTokens   int            `json:"input_tokens"`
				MaxTokens     int            `json:"max_tokens"`
				Result        map[string]any `json:"result"`
				Error         string         `json:"error"`
				BenchmarkID   string         `json:"benchmark_id,omitempty"`
				ConfigID      string         `json:"config_id,omitempty"`
				EngineVersion string         `json:"engine_version,omitempty"`
				EngineImage   string         `json:"engine_image,omitempty"`
				ResourceUsage map[string]any `json:"resource_usage,omitempty"`
			} `json:"cells"`
		} `json:"matrix_profiles"`
	}
	if err := json.Unmarshal([]byte(matrixJSON), &payload); err != nil {
		return nil
	}
	var entries []BenchmarkEntry
	for _, profile := range payload.MatrixProfiles {
		for _, cell := range profile.Cells {
			entry := BenchmarkEntry{
				Profile:       profile.Label,
				Concurrency:   cell.Concurrency,
				InputTokens:   cell.InputTokens,
				MaxTokens:     cell.MaxTokens,
				BenchmarkID:   cell.BenchmarkID,
				ConfigID:      cell.ConfigID,
				EngineVersion: cell.EngineVersion,
				EngineImage:   cell.EngineImage,
				ResourceUsage: cloneAnyMap(cell.ResourceUsage),
				Error:         cell.Error,
			}
			if cell.Result != nil {
				entry.ThroughputTPS = primaryBenchmarkRate(cell.Result)
				entry.TTFTP95Ms = readFloatField(cell.Result, "ttft_p95_ms")
				entry.TPOTP95Ms = readFloatField(cell.Result, "tpot_p95_ms")
			}
			entries = append(entries, entry)
		}
	}
	return entries
}

func readFloatField(summary map[string]any, key string) float64 {
	if summary == nil {
		return 0
	}
	if value, ok := summary[key].(float64); ok {
		return value
	}
	return 0
}

func primaryBenchmarkRate(summary map[string]any) float64 {
	for _, key := range []string{
		"throughput_tps",
		"images_per_sec",
		"reranks_per_sec",
		"embeddings_per_sec",
		"asr_throughput",
		"qps",
	} {
		if value := readFloatField(summary, key); value > 0 {
			return value
		}
	}
	return 0
}

func readBenchmarkMetrics(summary map[string]any, result *HarvestResult) {
	if result == nil {
		return
	}
	if tp := primaryBenchmarkRate(summary); tp > 0 {
		result.Throughput = tp
	}
	if ttft := readFloatField(summary, "ttft_p95_ms"); ttft > 0 {
		result.TTFTP95 = ttft
	}
	if tpot := readFloatField(summary, "tpot_p95_ms"); tpot > 0 {
		result.TPOTP95 = tpot
	}
	if vram := readFloatField(summary, "vram_usage_mib"); vram > 0 {
		result.VRAMMiB = vram
	}
}
