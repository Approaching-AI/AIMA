package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
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
//
// DC-8: the frontier counters below are snapshots captured at the last plan
// cycle. They describe different things and should not be compared with
// counts emitted from logs or plan.md without noting the source:
//   - LastPlanKnowledgeGaps: knowledge-level gaps (model×engine with zero benchmarks)
//   - LastPlanReadyCombos:   model×engine pairs the planner deemed eligible for new tasks this cycle
//   - LastPlanBlockedCombos: pairs filtered out by resolver/runtime checks
//
// The authoritative per-phase view is available-combos.md inside the workspace.
type ExplorerStatus struct {
	Running               bool           `json:"running"`
	Enabled               bool           `json:"enabled"`
	Phase                 string         `json:"phase"`
	Tier                  int            `json:"tier"`
	ActivePlan            *ExplorerPlan  `json:"active_plan,omitempty"`
	Schedule              ScheduleConfig `json:"schedule"`
	LastRun               time.Time      `json:"last_run,omitempty"`
	Mode                  string         `json:"mode"`
	RoundsUsed            int            `json:"rounds_used"`
	MaxRounds             int            `json:"max_rounds"`
	TokensUsedToday       int            `json:"tokens_used_today"`
	MaxTokensPerDay       int            `json:"max_tokens_per_day"`
	MaxCycles             int            `json:"max_cycles"`
	MaxTasks              int            `json:"max_tasks"`
	LastPlanMetrics       *PlanMetrics   `json:"last_plan_metrics,omitempty"`
	BlockedByDeploys      []DeployStatus `json:"blocked_by_deploys,omitempty"`
	LastPlanKnowledgeGaps int            `json:"last_plan_knowledge_gaps,omitempty"`
	LastPlanReadyCombos   int            `json:"last_plan_ready_combos,omitempty"`
	LastPlanBlockedCombos int            `json:"last_plan_blocked_combos,omitempty"`
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

var errorExcerptRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*Error:\s*[^\n]+`)

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

	// Per-task cleanup: tears down a single deployment by name. Called at the
	// end of each task in executePlan to prevent one task's deploy from
	// starving the next task's deploy of GPU memory (Bug-8).
	cleanupModelDeploy func(ctx context.Context, name string) error

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

	// DC-8: last plan cycle frontier counters, exposed via Status().
	lastPlanKnowledgeGaps int
	lastPlanReadyCombos   int
	lastPlanBlockedCombos int
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

// WithCleanupModelDeploy sets the per-task teardown hook invoked after each
// task in executePlan. Prevents GPU starvation of subsequent tasks (Bug-8).
func WithCleanupModelDeploy(fn func(ctx context.Context, name string) error) ExplorerOption {
	return func(e *Explorer) { e.cleanupModelDeploy = fn }
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

func (e *Explorer) writeClosedPlanDocument(status string, metrics *PlanMetrics) {
	if e.workspace == nil {
		return
	}
	if err := e.workspace.Init(); err != nil {
		slog.Warn("explorer: init workspace for closed plan failed", "error", err)
		return
	}
	if err := e.workspace.WriteClosedPlanDocument(status, metrics); err != nil {
		slog.Warn("explorer: write closed plan failed", "status", status, "error", err)
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
			WithAgentPhaseObserver(e.setPhase),
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
		Running:               e.running,
		Enabled:               e.config.Enabled,
		Phase:                 e.phase,
		Tier:                  e.tier,
		ActivePlan:            e.activePlan,
		Schedule:              e.config.Schedule,
		LastRun:               e.lastRun,
		Mode:                  e.config.Mode,
		RoundsUsed:            e.roundsUsed,
		MaxRounds:             e.config.MaxRounds,
		TokensUsedToday:       e.tokensUsedToday,
		MaxTokensPerDay:       e.config.MaxTokensPerDay,
		MaxCycles:             e.config.MaxCycles,
		MaxTasks:              e.config.MaxTasks,
		LastPlanMetrics:       e.lastPlanMetrics,
		BlockedByDeploys:      e.blockedByDeploys,
		LastPlanKnowledgeGaps: e.lastPlanKnowledgeGaps,
		LastPlanReadyCombos:   e.lastPlanReadyCombos,
		LastPlanBlockedCombos: e.lastPlanBlockedCombos,
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
	readyCombos, blockedCombos, _ := comboFactCounts(*input)
	e.mu.Lock()
	e.lastPlanKnowledgeGaps = len(input.Gaps)
	e.lastPlanReadyCombos = readyCombos
	e.lastPlanBlockedCombos = blockedCombos
	e.mu.Unlock()
	slog.Info("explorer: plan input ready",
		"knowledge_gaps", len(input.Gaps),
		"ready_combos", readyCombos,
		"blocked_combos", blockedCombos,
		"deploys", len(input.ActiveDeploys),
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
	proposedTasks := len(plan.Tasks)
	if e.db != nil {
		var dedupFiltered []PlanTask
		for _, t := range plan.Tasks {
			if t.SourceRef != "" {
				dedupFiltered = append(dedupFiltered, t)
				continue
			}
			completed, _ := e.db.HasCompletedExploration(ctx, t.Model, t.Engine)
			if completed && !hasPendingWorkFor(input.PendingWork, t.Model, t.Engine) {
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

	slog.Info("explorer: plan generated",
		"id", plan.ID,
		"tier", plan.Tier,
		"reasoning", plan.Reasoning,
		"tasks", len(plan.Tasks),
		"task_list", planTaskSummaries(plan.Tasks),
		"llm_tokens", planTokens,
		"proposed_tasks", proposedTasks,
		"dedup_dropped", proposedTasks-len(plan.Tasks),
		"ready_combos_seen", readyCombos,
		"blocked_combos_seen", blockedCombos,
		"knowledge_gaps", len(input.Gaps))
	if len(plan.Tasks) == 0 {
		slog.Info("explorer: no tasks to execute after filtering")
		// N1: empty plan still counts as a budget round — prevents infinite
		// LLM calls when all proposed tasks are deduped.
		enabled := true
		roundsUsed, _ := e.claimPlanRound(ctx, mode, maxRounds)
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
		closedPhase := "idle"
		if mode == "once" {
			closedPhase = "once_complete"
		} else if mode == "budget" && maxRounds > 0 && roundsUsed >= maxRounds {
			closedPhase = "budget_exhausted"
		}
		e.writeClosedPlanDocument(closedPhase, nil)
		e.setPhase(closedPhase)
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
			e.writeClosedPlanDocument("budget_exhausted", e.lastPlanMetrics)
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
	executedPlans := []*ExplorerPlan{plan}
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
			executedPlans = append(executedPlans, extraPlan)
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

	metrics := e.computePlanMetrics(executedPlans, elapsed, tokensAfter-tokensBefore)
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
	e.writeClosedPlanDocument(e.Status().Phase, metrics)
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
		var comboFacts []ComboFact
		if gathered, err := e.gatherComboFacts(ctx, hardware, models, engines); err == nil {
			comboFacts = gathered
			if reason, blocked := taskBlockedByExecutableFacts(task.Model, task.Engine, comboFacts, nil); blocked {
				return false, reason
			}
		}
		pendingWork := e.derivePendingWork(ctx, hardware, models, engines, comboFacts)
		if e.db != nil {
			completed, _ := e.db.HasCompletedExploration(ctx, task.Model, task.Engine)
			if completed && !hasPendingWorkFor(pendingWork, task.Model, task.Engine) {
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

// planTaskSummaries renders a short "kind:model/engine" per task so the
// "explorer: plan generated" log line carries the decision trace without
// callers having to post-join events.
func planTaskSummaries(tasks []PlanTask) []string {
	if len(tasks) == 0 {
		return nil
	}
	out := make([]string, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, fmt.Sprintf("%s:%s/%s", t.Kind, t.Model, t.Engine))
	}
	return out
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

func advisoryPlanStatus(result HarvestResult) string {
	if result.Success {
		return "completed"
	}
	if result.Cancelled {
		return "cancelled"
	}
	return "rejected"
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
	plan.Tasks[0].Status = advisoryPlanStatus(result)

	// Stamp the bench row with the advisory ID so Central's feedback ingest can
	// attribute the evidence. Best-effort: stamping is not load-bearing for the
	// feedback POST itself (which uses advisory.ID), only for downstream audit.
	if e.db != nil && result.BenchmarkID != "" && advisory.ID != "" {
		if err := e.db.UpdateBenchmarkAdvisoryID(ctx, result.BenchmarkID, advisory.ID); err != nil {
			slog.Debug("explorer: stamp advisory_id on benchmark failed", "error", err,
				"benchmark_id", result.BenchmarkID, "advisory_id", advisory.ID)
		}
	}

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

	terminalStatus := advisoryPlanStatus(result)
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

		// Bug-8: task-boundary teardown. The exploration manager releases its
		// own lease when it created the deploy, but tune promotes a best-config
		// redeploy and validate can reuse an existing ready deploy without
		// releasing it. Force-delete the task's model so the next task sees a
		// clean GPU, regardless of who owns the running container.
		if e.cleanupModelDeploy != nil && task.Model != "" {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := e.cleanupModelDeploy(cleanupCtx, task.Model); err != nil {
				// "deployment not found" is the common case after a successful
				// validate (exploration manager already released the lease) and
				// after many failed deploys (nothing was ever created). Only
				// surface as WARN when the error is likely actionable.
				msg := err.Error()
				if strings.Contains(msg, "not found") || strings.Contains(msg, "no deployment") {
					slog.Debug("explorer: task-boundary cleanup skipped (already torn down)",
						"model", task.Model, "detail", msg)
				} else {
					slog.Warn("explorer: task-boundary cleanup failed",
						"model", task.Model, "error", err)
				}
			}
			cancel()
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

// pickSingleBenchmarkProfile selects one profile out of the default latency/
// throughput pair so validate tasks don't execute overlapping cells twice
// (Bug-5/DC-7). For kinds with an obvious intent ("validate_long_context"),
// pick the latency profile (broader input coverage). If callers want both,
// they should author BenchmarkSpec themselves in the plan task.
func pickSingleBenchmarkProfile(profiles []ExplorationBenchmarkProfile, kind string) []ExplorationBenchmarkProfile {
	if len(profiles) <= 1 {
		return profiles
	}
	preferred := "latency"
	if strings.Contains(strings.ToLower(kind), "throughput") {
		preferred = "throughput"
	}
	for _, p := range profiles {
		if strings.EqualFold(p.Label, preferred) {
			return []ExplorationBenchmarkProfile{p}
		}
	}
	return profiles[:1]
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
	if len(spec.InputTokens) == 1 {
		p.InputTokens = spec.InputTokens[0]
	}
	if len(spec.MaxTokens) == 1 {
		p.MaxTokens = spec.MaxTokens[0]
	}
	return p
}

func taskSpecFromPlanTask(task PlanTask) TaskSpec {
	return TaskSpec{
		Kind:         task.Kind,
		Model:        task.Model,
		Engine:       task.Engine,
		EngineParams: task.Params,
		SearchSpace:  cloneSearchSpace(task.SearchSpace),
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

func effectiveTaskMaxModelLen(task PlanTask, model *LocalModel) int {
	if override := extractMaxModelLen(task.Params); override > 0 {
		return override
	}
	if model != nil && model.MaxContextLen > 0 {
		return model.MaxContextLen
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
	selected := append([]int{}, filtered...)
	highest := bestBenchmarkTokenLevel(maxAllowed)
	if highest > 0 && highest > maxInt(selected) && !containsInt(selected, highest) {
		selected = append(selected, highest)
	}
	if len(selected) >= 3 {
		return normalizeTokenLevels(selected)
	}
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

func maxInt(vals []int) int {
	if len(vals) == 0 {
		return 0
	}
	m := vals[0]
	for _, v := range vals[1:] {
		if v > m {
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

	var searchSpace map[string][]any
	if len(task.SearchSpace) > 0 {
		searchSpace = cloneSearchSpace(task.SearchSpace)
	}
	var engineParams map[string]any
	if len(task.Params) > 0 {
		engineParams = make(map[string]any, len(task.Params))
		for k, v := range task.Params {
			engineParams[k] = v
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
	if engineParams != nil {
		req.EngineParams = engineParams
	}

	var taskModelMeta *LocalModel
	// Populate model metadata from local inventory for accurate overlay YAML
	if e.gatherLocalModels != nil {
		if models, err := e.gatherLocalModels(ctx); err == nil {
			for _, m := range models {
				if strings.EqualFold(m.Name, task.Model) {
					model := m
					taskModelMeta = &model
					req.Target.ModelType = m.Type
					req.Target.Family = m.Family
					req.Target.ParameterCount = m.ParameterCount
					break
				}
			}
		}
	}

	// DC-1: long-context probes need the deploy's max_model_len to match the
	// model's real capacity. Otherwise the planner's conservative default caps
	// the probe and the benchmark silently tops out below target. Inject
	// max_model_len into search space when the task is a long-context probe
	// and the caller didn't already set it.
	if task.Kind == "validate_long_context" && taskModelMeta != nil && taskModelMeta.MaxContextLen > 0 {
		if req.SearchSpace == nil {
			req.SearchSpace = map[string][]any{}
		}
		hasMaxLen := false
		for _, key := range []string{"max_model_len", "context_length", "ctx_size", "max_context_tokens"} {
			if _, ok := req.SearchSpace[key]; ok {
				hasMaxLen = true
				break
			}
		}
		if !hasMaxLen {
			req.SearchSpace["max_model_len"] = []any{taskModelMeta.MaxContextLen}
			slog.Info("explorer: long-context probe set max_model_len to model capacity",
				"model", task.Model, "max_context_len", taskModelMeta.MaxContextLen)
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
				// Bug-5/DC-7: pick a single profile per validate task to avoid
				// running overlapping cells twice. Default picks the latency
				// profile (broader input coverage, concurrency=1) unless the
				// task explicitly declares throughput intent.
				profiles := e.resolveBenchmarkProfiles(hw)
				req.BenchmarkProfiles = pickSingleBenchmarkProfile(profiles, task.Kind)
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
	if maxModelLen := effectiveTaskMaxModelLen(task, taskModelMeta); maxModelLen > 0 {
		req.BenchmarkProfiles = adaptBenchmarkProfiles(req.BenchmarkProfiles, maxModelLen)
	}

	status, err := e.explMgr.StartAndWait(ctx, req)
	cancelled := err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
	if status == nil {
		return HarvestResult{Success: false, Cancelled: cancelled, Error: err.Error()}
	}
	result := e.parseExplorationResult(status)
	switch {
	case err != nil:
		result.Success = false
		result.Cancelled = cancelled
		result.Error = err.Error()
		return result
	case status.Run.Status == "failed":
		result.Success = false
		result.Error = status.Run.Error
		return result
	case status.Run.Status == "cancelled":
		result.Success = false
		result.Cancelled = true
		result.Error = status.Run.Error
		return result
	}

	// Bug-6: record deploy-applied config (post safety-cap) for the harvest note
	// and experiment file, not just planner-requested Params.
	result.Config = mergeConfigPreferringApplied(task.Params, result.DeployConfig)
	return result
}

// mergeConfigPreferringApplied returns planner params with any key that also
// appears in the deploy-applied config replaced by the applied value. This
// preserves planner intent for fields the deploy path doesn't echo back while
// recording safety-cap adjustments (e.g. gmu=0.9 → 0.86) for the knowledge layer.
func mergeConfigPreferringApplied(requested, applied map[string]any) map[string]any {
	if len(requested) == 0 && len(applied) == 0 {
		return nil
	}
	out := make(map[string]any, len(requested)+len(applied))
	for k, v := range requested {
		out[k] = v
	}
	for k, v := range applied {
		out[k] = v
	}
	return out
}

func (e *Explorer) parseExplorationResult(status *ExplorationStatus) HarvestResult {
	result := HarvestResult{Success: true}
	// Parse summary JSON for throughput/latency data
	if status.Run.SummaryJSON != "" {
		var summary map[string]any
		if err := json.Unmarshal([]byte(status.Run.SummaryJSON), &summary); err == nil {
			readBenchmarkMetrics(summary, &result)
			readBenchmarkConfig(summary, &result)
			if nested, ok := summary["result"].(map[string]any); ok {
				readBenchmarkMetrics(nested, &result)
				readBenchmarkConfig(nested, &result)
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
	}
	input.PendingWork = e.derivePendingWork(ctx, input.Hardware, input.LocalModels, input.LocalEngines, input.ComboFacts)
	if e.db != nil {
		combos, _ := e.db.ListExploredCombos(ctx)
		for _, c := range combos {
			if !c.Completed || hasPendingWorkFor(input.PendingWork, c.Model, c.Engine) {
				continue
			}
			input.SkipCombos = append(input.SkipCombos, SkipCombo{
				Model:  c.Model,
				Engine: c.Engine,
				Reason: "completed",
			})
		}
		for _, r := range input.History {
			if r.Status != "completed" || hasPendingWorkFor(input.PendingWork, r.ModelID, r.EngineID) {
				continue
			}
			input.SkipCombos = append(input.SkipCombos, SkipCombo{
				Model:  r.ModelID,
				Engine: r.EngineID,
				Reason: "recently completed",
			})
		}
	}
	historyForBlockers := input.History
	if e.db != nil {
		if runs, err := e.db.ListExplorationRuns(ctx, "", 200); err == nil {
			historyForBlockers = make([]ExplorationRun, 0, len(runs))
			for _, run := range runs {
				if run == nil {
					continue
				}
				historyForBlockers = append(historyForBlockers, *run)
			}
		}
	}
	var records []ExperimentRecord
	if e.workspace != nil {
		if loaded, err := e.workspace.LoadExperimentRecords(); err == nil {
			records = loaded
		}
	}
	input.SkipCombos = append(input.SkipCombos,
		deriveFamilyArtifactSkipCombos(input.LocalModels, input.LocalEngines, input.ComboFacts, input.SkipCombos, records, historyForBlockers)...)
	for _, skip := range deriveModelScopeSkipCombos(input.ComboFacts, input.SkipCombos) {
		input.SkipCombos = append(input.SkipCombos, skip)
	}
	input.SkipCombos = dedupeSkipCombos(input.SkipCombos)
	input.PendingWork = filterPendingWork(input.PendingWork, input.ComboFacts, input.SkipCombos)

	return input, nil
}

func hasPendingWorkFor(pending []PendingWork, model, engine string) bool {
	for _, work := range pending {
		if strings.EqualFold(strings.TrimSpace(work.Model), strings.TrimSpace(model)) &&
			strings.EqualFold(strings.TrimSpace(work.Engine), strings.TrimSpace(engine)) {
			return true
		}
	}
	return false
}

func filterPendingWork(pending []PendingWork, comboFacts []ComboFact, skipCombos []SkipCombo) []PendingWork {
	if len(pending) == 0 {
		return nil
	}
	filtered := make([]PendingWork, 0, len(pending))
	for _, work := range pending {
		if _, blocked := taskBlockedByExecutableFacts(work.Model, work.Engine, comboFacts, skipCombos); blocked {
			continue
		}
		filtered = append(filtered, work)
	}
	return filtered
}

type comboCoverageState struct {
	HasMeaningfulBenchmark bool
	MaxSuccessfulInput     int
	HasCompletedTune       bool
}

func (e *Explorer) derivePendingWork(ctx context.Context, hardware HardwareInfo, models []LocalModel, engines []LocalEngine, comboFacts []ComboFact) []PendingWork {
	readySet := make(map[string]ComboFact, len(comboFacts))
	for _, fact := range comboFacts {
		if !strings.EqualFold(strings.TrimSpace(fact.Status), "ready") {
			continue
		}
		key := planTaskComboKey(fact.Model, fact.Engine)
		if key == "" {
			continue
		}
		readySet[strings.ToLower(key)] = fact
	}
	if len(readySet) == 0 {
		return nil
	}

	modelByName := make(map[string]LocalModel, len(models))
	for _, model := range models {
		modelByName[strings.ToLower(strings.TrimSpace(model.Name))] = model
	}
	engineByName := make(map[string]LocalEngine, len(engines)*2)
	for _, engine := range engines {
		for _, alias := range []string{engine.Name, engine.Type} {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias == "" {
				continue
			}
			engineByName[alias] = engine
		}
	}

	stateByCombo := make(map[string]comboCoverageState, len(readySet))
	if e.db != nil {
		runs, _ := e.db.ListExplorationRuns(ctx, "", 200)
		tunedByCombo := make(map[string]bool, len(runs))
		for _, run := range runs {
			if run == nil || !strings.EqualFold(run.Kind, "tune") || !strings.EqualFold(run.Status, "completed") {
				continue
			}
			key := strings.ToLower(planTaskComboKey(run.ModelID, run.EngineID))
			if key != "" {
				tunedByCombo[key] = true
			}
		}
		for key, fact := range readySet {
			cfgs, err := e.db.ListConfigurations(ctx, hardware.Profile, fact.Model, fact.Engine)
			if err != nil || len(cfgs) == 0 {
				stateByCombo[key] = comboCoverageState{HasCompletedTune: tunedByCombo[key]}
				continue
			}
			configIDs := make([]string, 0, len(cfgs))
			for _, cfg := range cfgs {
				if cfg == nil {
					continue
				}
				configIDs = append(configIDs, cfg.ID)
			}
			results, err := e.db.ListBenchmarkResults(ctx, configIDs, 0)
			if err != nil {
				stateByCombo[key] = comboCoverageState{HasCompletedTune: tunedByCombo[key]}
				continue
			}
			state := comboCoverageState{HasCompletedTune: tunedByCombo[key]}
			for _, result := range results {
				if !meaningfulBenchmarkRecord(result) {
					continue
				}
				state.HasMeaningfulBenchmark = true
				if inputTokens := parseBenchmarkBucketFloor(result.InputLenBucket); inputTokens > state.MaxSuccessfulInput {
					state.MaxSuccessfulInput = inputTokens
				}
			}
			stateByCombo[key] = state
		}
	}

	var pending []PendingWork
	for key, fact := range readySet {
		model := modelByName[strings.ToLower(strings.TrimSpace(fact.Model))]
		engine := engineByName[strings.ToLower(strings.TrimSpace(fact.Engine))]
		state := stateByCombo[key]
		if !state.HasMeaningfulBenchmark {
			pending = append(pending, PendingWork{
				Model:    fact.Model,
				Engine:   fact.Engine,
				Kind:     "validate_baseline",
				Reason:   "baseline benchmark evidence missing for this ready combo",
				Priority: 10,
			})
			continue
		}
		if longTarget := longContextProbeTarget(model.MaxContextLen); longTarget > 0 && state.MaxSuccessfulInput < longTarget {
			pending = append(pending, PendingWork{
				Model:  fact.Model,
				Engine: fact.Engine,
				Kind:   "validate_long_context",
				Reason: fmt.Sprintf("long-context coverage missing (best successful input=%d, target=%d)", state.MaxSuccessfulInput, longTarget),
				Benchmark: BenchmarkSpec{
					Concurrency:      []int{1},
					InputTokens:      []int{longTarget},
					MaxTokens:        []int{256},
					RequestsPerCombo: 3,
				},
				Priority: 20,
			})
		}
		searchSpace := suggestedTuneSearchSpace(model, engine)
		if !state.HasCompletedTune && len(searchSpace) > 0 {
			pending = append(pending, PendingWork{
				Model:       fact.Model,
				Engine:      fact.Engine,
				Kind:        "tune",
				Reason:      "baseline exists and tunable search space remains unexplored",
				SearchSpace: searchSpace,
				Benchmark: BenchmarkSpec{
					Concurrency:      []int{1},
					InputTokens:      []int{128},
					MaxTokens:        []int{256},
					RequestsPerCombo: 3,
				},
				Priority: 30,
			})
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].Priority != pending[j].Priority {
			return pending[i].Priority < pending[j].Priority
		}
		if pending[i].Model != pending[j].Model {
			return strings.ToLower(pending[i].Model) < strings.ToLower(pending[j].Model)
		}
		if pending[i].Engine != pending[j].Engine {
			return strings.ToLower(pending[i].Engine) < strings.ToLower(pending[j].Engine)
		}
		return pending[i].Kind < pending[j].Kind
	})
	return pending
}

func meaningfulBenchmarkRecord(result *state.BenchmarkResult) bool {
	if result == nil {
		return false
	}
	if result.ThroughputTPS > 0 || result.QPS > 0 {
		return true
	}
	for _, ptr := range []*float64{
		result.ImagesPerSec,
		result.AudioThroughput,
		result.ASRThroughput,
		result.RTFP50,
		result.VideosPerHour,
	} {
		if ptr != nil && *ptr > 0 {
			return true
		}
	}
	return false
}

func parseBenchmarkBucketFloor(bucket string) int {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return 0
	}
	for _, sep := range []string{"-", "_"} {
		if idx := strings.Index(bucket, sep); idx > 0 {
			bucket = bucket[:idx]
			break
		}
	}
	v, _ := strconv.Atoi(bucket)
	return v
}

func longContextProbeTarget(maxContext int) int {
	if maxContext <= 8192 {
		return 0
	}
	return bestBenchmarkTokenLevel(maxContext)
}

func suggestedTuneSearchSpace(model LocalModel, engine LocalEngine) map[string][]any {
	if len(engine.TunableParams) == 0 {
		return nil
	}
	space := make(map[string][]any)
	for _, key := range []string{"gpu_memory_utilization", "mem_fraction_static"} {
		if _, ok := engine.TunableParams[key]; ok {
			space[key] = []any{0.7, 0.75, 0.8, 0.85, 0.9}
		}
	}
	if model.MaxContextLen > 0 {
		for _, key := range []string{"max_model_len", "context_length", "ctx_size", "max_context_tokens"} {
			if _, ok := engine.TunableParams[key]; !ok {
				continue
			}
			values := suggestedContextSearchValues(extractMaxModelLen(engine.TunableParams), model.MaxContextLen)
			if len(values) > 1 {
				space[key] = values
			}
		}
	}
	if len(space) == 0 {
		return nil
	}
	return space
}

func suggestedContextSearchValues(current, maxAllowed int) []any {
	if maxAllowed <= 0 {
		return nil
	}
	if current <= 0 {
		current = bestBenchmarkTokenLevel(minInt([]int{maxAllowed, 8192}))
	}
	high := bestBenchmarkTokenLevel(maxAllowed)
	if current <= 0 || high <= 0 {
		return nil
	}
	values := []int{current}
	if high > current {
		if mid := bestBenchmarkTokenLevel((current + high) / 2); mid > current && mid < high {
			values = append(values, mid)
		}
		values = append(values, high)
	}
	values = normalizeTokenLevels(values)
	if len(values) <= 1 {
		return nil
	}
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func dedupeSkipCombos(skipCombos []SkipCombo) []SkipCombo {
	if len(skipCombos) <= 1 {
		return skipCombos
	}
	deduped := make([]SkipCombo, 0, len(skipCombos))
	seen := make(map[string]struct{}, len(skipCombos))
	for _, skip := range skipCombos {
		key := strings.ToLower(strings.TrimSpace(skip.Model)) + "|" + strings.ToLower(strings.TrimSpace(skip.Engine))
		if key == "|" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		deduped = append(deduped, skip)
	}
	sort.Slice(deduped, func(i, j int) bool {
		if deduped[i].Model != deduped[j].Model {
			return strings.ToLower(deduped[i].Model) < strings.ToLower(deduped[j].Model)
		}
		if deduped[i].Engine != deduped[j].Engine {
			return strings.ToLower(deduped[i].Engine) < strings.ToLower(deduped[j].Engine)
		}
		return deduped[i].Reason < deduped[j].Reason
	})
	return deduped
}

func deriveModelScopeSkipCombos(comboFacts []ComboFact, skipCombos []SkipCombo) []SkipCombo {
	if len(comboFacts) == 0 {
		return nil
	}
	exactSkips := make(map[string]string, len(skipCombos))
	modelSkips := make(map[string]struct{}, len(skipCombos))
	for _, skip := range skipCombos {
		model := strings.TrimSpace(skip.Model)
		if model == "" {
			continue
		}
		if strings.TrimSpace(skip.Engine) == "" {
			modelSkips[strings.ToLower(model)] = struct{}{}
			continue
		}
		exactSkips[strings.ToLower(planTaskComboKey(model, skip.Engine))] = strings.TrimSpace(skip.Reason)
	}

	type readyCombo struct {
		engine string
		reason string
	}
	readyByModel := make(map[string][]readyCombo)
	for _, fact := range comboFacts {
		if !strings.EqualFold(strings.TrimSpace(fact.Status), "ready") {
			continue
		}
		model := strings.TrimSpace(fact.Model)
		engine := strings.TrimSpace(fact.Engine)
		if model == "" || engine == "" {
			continue
		}
		reason := exactSkips[strings.ToLower(planTaskComboKey(model, engine))]
		readyByModel[model] = append(readyByModel[model], readyCombo{engine: engine, reason: reason})
	}

	var derived []SkipCombo
	for model, combos := range readyByModel {
		if _, exists := modelSkips[strings.ToLower(model)]; exists {
			continue
		}
		if len(combos) == 0 {
			continue
		}
		allExhausted := true
		reasons := make([]string, 0, len(combos))
		seenReasons := make(map[string]struct{}, len(combos))
		for _, combo := range combos {
			if strings.TrimSpace(combo.reason) == "" {
				allExhausted = false
				break
			}
			if _, exists := seenReasons[combo.reason]; !exists {
				seenReasons[combo.reason] = struct{}{}
				reasons = append(reasons, combo.reason)
			}
		}
		if !allExhausted {
			continue
		}
		reason := "all ready combos exhausted on this device"
		if len(reasons) == 1 {
			reason = reasons[0]
		}
		derived = append(derived, SkipCombo{
			Model:  model,
			Reason: reason,
		})
	}
	sort.Slice(derived, func(i, j int) bool {
		return strings.ToLower(derived[i].Model) < strings.ToLower(derived[j].Model)
	})
	return derived
}

func deriveFamilyArtifactSkipCombos(localModels []LocalModel, localEngines []LocalEngine, comboFacts []ComboFact, skipCombos []SkipCombo, records []ExperimentRecord, runs []ExplorationRun) []SkipCombo {
	if len(records) == 0 && len(runs) == 0 || len(comboFacts) == 0 {
		return nil
	}

	type blockerGroup struct {
		family     string
		engine     string
		artifact   string
		summary    string
		models     map[string]struct{}
		modelCount int
	}

	familyByModel := make(map[string]string, len(localModels))
	for _, model := range localModels {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		family := strings.TrimSpace(model.Family)
		if family == "" {
			family = inferModelFamily(name)
		}
		familyByModel[strings.ToLower(name)] = family
	}

	engineArtifacts := make(map[string]string, len(localEngines)*2)
	for _, engine := range localEngines {
		artifact := strings.TrimSpace(engine.Artifact)
		if artifact == "" {
			continue
		}
		for _, alias := range []string{engine.Name, engine.Type} {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			engineArtifacts[strings.ToLower(alias)] = artifact
		}
	}

	comboArtifacts := make(map[string]string, len(comboFacts))
	for _, fact := range comboFacts {
		key := strings.ToLower(planTaskComboKey(fact.Model, fact.Engine))
		if key == "" {
			continue
		}
		artifact := strings.TrimSpace(fact.Artifact)
		if artifact == "" {
			artifact = engineArtifacts[strings.ToLower(strings.TrimSpace(fact.Engine))]
		}
		comboArtifacts[key] = artifact
	}

	exactSkips := make(map[string]struct{}, len(skipCombos))
	modelSkips := make(map[string]struct{}, len(skipCombos))
	for _, skip := range skipCombos {
		model := strings.ToLower(strings.TrimSpace(skip.Model))
		if model == "" {
			continue
		}
		if strings.TrimSpace(skip.Engine) == "" {
			modelSkips[model] = struct{}{}
			continue
		}
		exactSkips[strings.ToLower(planTaskComboKey(skip.Model, skip.Engine))] = struct{}{}
	}

	groupBySignature := make(map[string]*blockerGroup)
	groupByFrontier := make(map[string][]*blockerGroup)
	addObservation := func(model, engine, artifact, signature, summary string) {
		model = strings.TrimSpace(model)
		engine = strings.TrimSpace(engine)
		artifact = strings.TrimSpace(artifact)
		if model == "" || engine == "" || artifact == "" || signature == "" {
			return
		}
		family := strings.TrimSpace(familyByModel[strings.ToLower(model)])
		if family == "" {
			family = inferModelFamily(model)
		}
		if family == "" || family == "unknown" {
			return
		}
		groupKey := strings.ToLower(family) + "|" + strings.ToLower(engine) + "|" + strings.ToLower(artifact) + "|" + signature
		group := groupBySignature[groupKey]
		if group == nil {
			group = &blockerGroup{
				family:   family,
				engine:   engine,
				artifact: artifact,
				summary:  summary,
				models:   make(map[string]struct{}),
			}
			groupBySignature[groupKey] = group
			frontierKey := strings.ToLower(family) + "|" + strings.ToLower(engine) + "|" + strings.ToLower(artifact)
			groupByFrontier[frontierKey] = append(groupByFrontier[frontierKey], group)
		}
		modelKey := strings.ToLower(model)
		if _, exists := group.models[modelKey]; !exists {
			group.models[modelKey] = struct{}{}
			group.modelCount++
		}
	}
	for _, rec := range records {
		signature, summary := experimentFailureSignature(rec)
		if signature == "" {
			continue
		}
		model := strings.TrimSpace(rec.Task.Model)
		engine := strings.TrimSpace(rec.Task.Engine)
		comboKey := strings.ToLower(planTaskComboKey(model, engine))
		artifact := strings.TrimSpace(comboArtifacts[comboKey])
		if artifact == "" {
			artifact = strings.TrimSpace(rec.Result.EngineImage)
		}
		if artifact == "" {
			artifact = strings.TrimSpace(engineArtifacts[strings.ToLower(engine)])
		}
		addObservation(model, engine, artifact, signature, summary)
	}
	for _, run := range runs {
		signature, summary := explorationRunFailureSignature(run)
		if signature == "" {
			continue
		}
		engine := strings.TrimSpace(run.EngineID)
		model := strings.TrimSpace(run.ModelID)
		artifact := strings.TrimSpace(engineArtifacts[strings.ToLower(engine)])
		if artifact == "" {
			continue
		}
		addObservation(model, engine, artifact, signature, summary)
	}

	var derived []SkipCombo
	seenDerived := make(map[string]struct{})
	for _, fact := range comboFacts {
		if !strings.EqualFold(strings.TrimSpace(fact.Status), "ready") {
			continue
		}
		model := strings.TrimSpace(fact.Model)
		engine := strings.TrimSpace(fact.Engine)
		if model == "" || engine == "" {
			continue
		}
		modelKey := strings.ToLower(model)
		if _, exists := modelSkips[modelKey]; exists {
			continue
		}
		comboKey := strings.ToLower(planTaskComboKey(model, engine))
		if _, exists := exactSkips[comboKey]; exists {
			continue
		}
		family := strings.TrimSpace(familyByModel[modelKey])
		if family == "" {
			family = inferModelFamily(model)
		}
		if family == "" || family == "unknown" {
			continue
		}
		artifact := strings.TrimSpace(fact.Artifact)
		if artifact == "" {
			artifact = strings.TrimSpace(comboArtifacts[comboKey])
		}
		if artifact == "" {
			artifact = strings.TrimSpace(engineArtifacts[strings.ToLower(engine)])
		}
		if artifact == "" {
			continue
		}
		frontierKey := strings.ToLower(family) + "|" + strings.ToLower(engine) + "|" + strings.ToLower(artifact)
		groups := groupByFrontier[frontierKey]
		var best *blockerGroup
		for _, group := range groups {
			if group == nil || group.modelCount < 2 {
				continue
			}
			if best == nil || group.modelCount > best.modelCount {
				best = group
			}
		}
		if best == nil {
			continue
		}
		if _, exists := seenDerived[comboKey]; exists {
			continue
		}
		reason := fmt.Sprintf("repeated structural failure across %d %s-family models on %s: %s",
			best.modelCount, best.family, best.artifact, best.summary)
		derived = append(derived, SkipCombo{
			Model:  model,
			Engine: engine,
			Reason: reason,
		})
		seenDerived[comboKey] = struct{}{}
	}
	return derived
}

func experimentFailureSignature(rec ExperimentRecord) (string, string) {
	signal := experimentSignal(rec)
	switch signal {
	case "", "benchmark_ok", "completed", "unknown":
		return "", ""
	case "inference_no_output":
		return signal, "benchmark matrix returned no successful output"
	}
	summary := extractFailureSummary(rec.Result.Error)
	if summary == "" {
		return "", ""
	}
	return signal + "|" + strings.ToLower(summary), summary
}

func explorationRunFailureSignature(run ExplorationRun) (string, string) {
	status := strings.ToLower(strings.TrimSpace(run.Status))
	if status != "failed" {
		return "", ""
	}
	summary := extractFailureSummary(run.Error)
	if summary == "" {
		return "", ""
	}
	return "failed|" + strings.ToLower(summary), summary
}

func extractFailureSummary(errText string) string {
	errText = strings.TrimSpace(errText)
	if errText == "" {
		return ""
	}
	matches := errorExcerptRe.FindAllString(errText, -1)
	if len(matches) > 0 {
		return compactFailureSummary(matches[len(matches)-1])
	}
	lines := strings.Split(errText, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := compactFailureSummary(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func compactFailureSummary(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	text = strings.Join(strings.Fields(text), " ")
	for _, marker := range []string{"ModuleNotFoundError:", "ValueError:", "RuntimeError:", "TypeError:", "AssertionError:"} {
		if idx := strings.LastIndex(text, marker); idx >= 0 {
			text = text[idx:]
			break
		}
	}
	if len(text) > 180 {
		text = text[:177] + "..."
	}
	return text
}

func (e *Explorer) computePlanMetrics(plans []*ExplorerPlan, elapsed time.Duration, tokensUsed int) *PlanMetrics {
	m := &PlanMetrics{
		DurationS:  elapsed.Seconds(),
		TokensUsed: tokensUsed,
	}
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		m.TotalTasks += len(plan.Tasks)
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
		// Bug-4: rounds_used is a runtime counter, not a setting. Allowing users
		// to overwrite it conflates persistent state with config and enables
		// trivial budget resets. Use a separate reset tool if needed.
		e.mu.Unlock()
		return "", fmt.Errorf("rounds_used is read-only (runtime counter, not a setting)")
	default:
		e.mu.Unlock()
		return "", fmt.Errorf("unknown explorer config key %q", key)
	}

	e.config.Schedule = normalizeScheduleConfig(e.config.Schedule)
	schedule := e.config.Schedule
	normalized := e.configValueLocked(key)
	// Bug-1: max_cycles / max_tasks are captured by the planner at construction.
	// Rebuild it so the next plan phase honors the new budget instead of the
	// value that was current at explorer startup.
	if key == "max_cycles" || key == "max_tasks" {
		e.setupPlannerLocked()
	}
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
			LatencyP50Ms:  r.LatencyP50,
			TTFTP50Ms:     r.TTFTP50,
			TTFTP95Ms:     r.TTFTP95,
			TPOTP50Ms:     r.TPOTP50,
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
				entry.LatencyP50Ms = primaryLatencyP50(cell.Result, cell.MaxTokens)
				entry.TTFTP50Ms = readFloatField(cell.Result, "ttft_p50_ms")
				entry.TTFTP95Ms = readFloatField(cell.Result, "ttft_p95_ms")
				entry.TPOTP50Ms = readFloatField(cell.Result, "tpot_p50_ms")
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

func primaryLatencyP50(summary map[string]any, maxTokens int) float64 {
	if summary == nil {
		return 0
	}
	for _, key := range []string{
		"latency_p50_ms",
		"embedding_latency_p50_ms",
		"rerank_latency_p50_ms",
	} {
		if value := readFloatField(summary, key); value > 0 {
			return value
		}
	}
	if value := readFloatField(summary, "video_latency_p50_s"); value > 0 {
		return value * 1000
	}
	ttft := readFloatField(summary, "ttft_p50_ms")
	tpot := readFloatField(summary, "tpot_p50_ms")
	if ttft <= 0 && tpot <= 0 {
		return 0
	}
	if maxTokens < 0 {
		maxTokens = 0
	}
	return ttft + (float64(maxTokens) * tpot)
}

func readBenchmarkMetrics(summary map[string]any, result *HarvestResult) {
	if result == nil {
		return
	}
	if tp := primaryBenchmarkRate(summary); tp > 0 {
		result.Throughput = tp
	}
	if ttft := readFloatField(summary, "ttft_p50_ms"); ttft > 0 {
		result.TTFTP50 = ttft
	}
	if ttft := readFloatField(summary, "ttft_p95_ms"); ttft > 0 {
		result.TTFTP95 = ttft
	}
	if tpot := readFloatField(summary, "tpot_p50_ms"); tpot > 0 {
		result.TPOTP50 = tpot
	}
	if tpot := readFloatField(summary, "tpot_p95_ms"); tpot > 0 {
		result.TPOTP95 = tpot
	}
	if latency := primaryLatencyP50(summary, result.MaxTokens); latency > 0 {
		result.LatencyP50 = latency
	}
	if vram := readFloatField(summary, "vram_usage_mib"); vram > 0 {
		result.VRAMMiB = vram
	}
}

func readBenchmarkConfig(summary map[string]any, result *HarvestResult) {
	if result == nil || summary == nil {
		return
	}
	cfg, _ := summary["config"].(map[string]any)
	if cfg == nil {
		return
	}
	if concurrency, ok := cfg["concurrency"].(float64); ok && result.Concurrency <= 0 {
		result.Concurrency = int(concurrency)
	}
	if inputTokens, ok := cfg["input_tokens"].(float64); ok && result.InputTokens <= 0 {
		result.InputTokens = int(inputTokens)
	}
	if maxTokens, ok := cfg["max_tokens"].(float64); ok && result.MaxTokens <= 0 {
		result.MaxTokens = int(maxTokens)
	}
	if latency := primaryLatencyP50(summary, result.MaxTokens); latency > 0 && result.LatencyP50 <= 0 {
		result.LatencyP50 = latency
	}
}
