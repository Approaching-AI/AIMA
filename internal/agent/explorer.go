package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
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
	Running    bool           `json:"running"`
	Enabled    bool           `json:"enabled"`
	Tier       int            `json:"tier"`
	ActivePlan *ExplorerPlan  `json:"active_plan,omitempty"`
	Schedule   ScheduleConfig `json:"schedule"`
	LastRun    time.Time      `json:"last_run,omitempty"`
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
	gatherHardware      func(ctx context.Context) (HardwareInfo, error)
	gatherGaps          func(ctx context.Context) ([]GapEntry, error)
	gatherDeploys       func(ctx context.Context) ([]DeployStatus, error)
	gatherOpenQuestions func(ctx context.Context) ([]OpenQuestion, error)
	gatherAdvisories    func(ctx context.Context) ([]Advisory, error)

	// Harvester callbacks, wired via options or buildToolDeps.
	syncPush func(ctx context.Context) error
	saveNote func(ctx context.Context, title, content, hardware, model, engine string) error

	// Advisory feedback callback, wired via WithAdvisoryFeedback.
	advisoryFeedback func(ctx context.Context, advisoryID, status, reason string) error

	mu               sync.RWMutex
	running          bool
	tier             int
	activePlan       *ExplorerPlan
	lastRun          time.Time
	cancel           context.CancelFunc
	activeExecutions int
	slotWaiters      chan struct{}
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

// WithAdvisoryFeedback sets the callback for sending advisory feedback to central.
func WithAdvisoryFeedback(fn func(ctx context.Context, advisoryID, status, reason string) error) ExplorerOption {
	return func(e *Explorer) { e.advisoryFeedback = fn }
}

func NewExplorer(config ExplorerConfig, agent *Agent, explMgr *ExplorationManager, db *state.DB, bus *EventBus, opts ...ExplorerOption) *Explorer {
	e := &Explorer{
		config:      config,
		agent:       agent,
		explMgr:     explMgr,
		db:          db,
		bus:         bus,
		slotWaiters: make(chan struct{}),
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
	opts := make([]HarvesterOption, 0, 3)
	if e.tier >= 2 && e.agent != nil && e.agent.llm != nil {
		opts = append(opts, WithHarvesterLLM(e.agent.llm))
	}
	if e.syncPush != nil {
		opts = append(opts, WithSyncPush(e.syncPush))
	}
	if e.saveNote != nil {
		opts = append(opts, WithSaveNote(e.saveNote))
	}
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
		if e.activeExecutions == 0 {
			e.activePlan = nil
		}
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
		Running:    e.running,
		Enabled:    e.config.Enabled,
		Tier:       e.tier,
		ActivePlan: e.activePlan,
		Schedule:   e.config.Schedule,
		LastRun:    e.lastRun,
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
	slog.Debug("explorer event", "type", ev.Type)

	// Re-detect tier periodically (LLM may have come online/offline)
	if e.refreshTier() {
		e.mu.RLock()
		currentTier := e.tier
		e.mu.RUnlock()
		slog.Info("explorer tier changed", "new", currentTier)
	}

	if !e.isEnabled() {
		slog.Debug("explorer disabled, skipping event", "type", ev.Type)
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
	input, err := e.buildPlanInput(ctx, &ev)
	if err != nil {
		slog.Warn("explorer: build plan input failed", "error", err)
		return
	}

	// Generate exploration plan
	planner := e.currentPlanner()
	plan, err := planner.Plan(ctx, *input)
	degraded := false
	if err != nil {
		slog.Warn("explorer: plan generation failed", "error", err)
		// If LLM planner failed, try rule planner fallback
		if tier >= 2 {
			slog.Info("explorer: LLM unavailable, degrading to Tier 1 planner")
			rp := &RulePlanner{}
			plan, err = rp.Plan(ctx, *input)
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

	go e.runPlan(ctx, plan)
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
		if err := e.acquireExecutionSlot(ctx); err != nil {
			return
		}
		defer e.releaseExecutionSlot()

		result := e.executeTask(ctx, task)

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
	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Skip tasks that require LLM if tier has degraded
		if taskRequiresLLM(task.Kind) && e.detectTier() < 2 {
			slog.Info("explorer: skipping LLM-only task (tier degraded)",
				"kind", task.Kind, "model", task.Model)
			task.Status = "skipped_tier_degraded"
			continue
		}

		slog.Info("explorer: executing task",
			"kind", task.Kind, "model", task.Model, "engine", task.Engine,
			"progress", fmt.Sprintf("%d/%d", i+1, len(plan.Tasks)))

		result := e.executeTask(ctx, *task)
		task.Status = "completed"

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

// taskRequiresLLM returns true for task kinds that need LLM reasoning.
func taskRequiresLLM(kind string) bool {
	return kind == "compare"
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
		Kind:      task.Kind,
		SourceRef: task.SourceRef,
		Target: ExplorationTarget{
			Hardware: task.Hardware,
			Model:    task.Model,
			Engine:   task.Engine,
		},
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

	// Recent exploration history
	if e.db != nil {
		runs, _ := e.db.ListExplorationRuns(ctx, "", 10)
		for _, r := range runs {
			input.History = append(input.History, *r)
		}
	}

	return input, nil
}

func (e *Explorer) runPlan(ctx context.Context, plan *ExplorerPlan) {
	if err := e.acquireExecutionSlot(ctx); err != nil {
		if e.db != nil {
			_ = e.db.UpdateExplorationPlan(ctx, &state.ExplorationPlanRow{
				ID:     plan.ID,
				Status: "cancelled",
			})
		}
		return
	}
	defer e.releaseExecutionSlot()

	e.executePlan(ctx, plan)
}

func (e *Explorer) acquireExecutionSlot(ctx context.Context) error {
	for {
		e.mu.Lock()
		maxRuns := e.config.Schedule.MaxConcurrentRuns
		if maxRuns <= 0 {
			maxRuns = 1
		}
		if e.activeExecutions < maxRuns {
			e.activeExecutions++
			e.mu.Unlock()
			return nil
		}
		wait := e.slotWaiters
		e.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
		}
	}
}

func (e *Explorer) releaseExecutionSlot() {
	e.mu.Lock()
	if e.activeExecutions > 0 {
		e.activeExecutions--
	}
	if e.activeExecutions == 0 {
		e.activePlan = nil
	}
	e.notifySlotWaitersLocked()
	e.mu.Unlock()
}

func (e *Explorer) notifySlotWaitersLocked() {
	old := e.slotWaiters
	e.slotWaiters = make(chan struct{})
	close(old)
}

func (e *Explorer) refreshTier() bool {
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
	default:
		e.mu.Unlock()
		return "", fmt.Errorf("unknown explorer config key %q", key)
	}

	e.config.Schedule = normalizeScheduleConfig(e.config.Schedule)
	schedule := e.config.Schedule
	normalized := e.configValueLocked(key)
	e.notifySlotWaitersLocked()
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
