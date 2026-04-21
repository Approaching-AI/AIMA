package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	state "github.com/jguan/aima/internal"
	"gopkg.in/yaml.v3"
)

const (
	gpuReleaseGrace                = 3 * time.Second // grace period for GPU memory to fully release from driver
	defaultStartAndWaitTimeout     = 30 * time.Minute
	extendedStartAndWaitTimeout    = 90 * time.Minute
	startAndWaitStopCleanupTimeout = 45 * time.Second
)

type ExplorationTarget struct {
	Hardware        string   `json:"hardware,omitempty"`
	GPUArch         string   `json:"gpu_arch,omitempty"` // e.g. "Ada" — for overlay YAML, not resolution
	Model           string   `json:"model"`
	Engine          string   `json:"engine,omitempty"`
	Runtime         string   `json:"runtime,omitempty"`
	ModelType       string   `json:"model_type,omitempty"`        // e.g. "llm", "asr", "tts", "embedding"
	InternalArgs    []string `json:"internal_args,omitempty"`     // engine params to exclude from overlay YAML
	HealthCheckPath string   `json:"health_check_path,omitempty"` // from engine YAML startup.health_check.path
	Family          string   `json:"family,omitempty"`            // from catalog metadata.family — overlay YAML
	ParameterCount  string   `json:"parameter_count,omitempty"`   // from catalog metadata.parameter_count — overlay YAML
}

type ExplorationConstraints struct {
	MaxCandidates int `json:"max_candidates,omitempty"`
}

type ExplorationBenchmarkProfile struct {
	Endpoint          string `json:"endpoint,omitempty"`
	Concurrency       int    `json:"concurrency,omitempty"` // legacy single-point (for tune tasks)
	InputTokens       int    `json:"input_tokens,omitempty"`
	MaxTokens         int    `json:"max_tokens,omitempty"`
	Rounds            int    `json:"rounds,omitempty"`
	RequestsPerCombo  int    `json:"requests_per_combo,omitempty"`
	ConcurrencyLevels []int  `json:"concurrency_levels,omitempty"`
	InputTokenLevels  []int  `json:"input_token_levels,omitempty"`
	MaxTokenLevels    []int  `json:"max_token_levels,omitempty"`
	Label             string `json:"label,omitempty"` // "latency", "throughput"
}

type ExplorationPlan struct {
	Kind              string                        `json:"kind"`
	Goal              string                        `json:"goal"`
	Target            ExplorationTarget             `json:"target"`
	SourceRef         string                        `json:"source_ref,omitempty"`
	EngineParams      map[string]any                `json:"engine_params,omitempty"`
	SearchSpace       map[string][]any              `json:"search_space,omitempty"`
	Constraints       ExplorationConstraints        `json:"constraints,omitempty"`
	BenchmarkProfiles []ExplorationBenchmarkProfile `json:"benchmark_profiles,omitempty"`
}

// isMatrixBenchmark returns true if any profile has matrix-level fields set.
func isMatrixBenchmark(profiles []ExplorationBenchmarkProfile) bool {
	for _, p := range profiles {
		if len(p.ConcurrencyLevels) > 0 {
			return true
		}
	}
	return false
}

// firstProfile returns the first benchmark profile, or a zero value if none.
func firstProfile(profiles []ExplorationBenchmarkProfile) ExplorationBenchmarkProfile {
	if len(profiles) > 0 {
		return profiles[0]
	}
	return ExplorationBenchmarkProfile{}
}

func benchmarkModality(modelType string) string {
	return strings.ToLower(strings.TrimSpace(modelType))
}

func startAndWaitFallbackTimeout(kind string) time.Duration {
	switch kind {
	case "validate", "open_question":
		return extendedStartAndWaitTimeout
	default:
		return defaultStartAndWaitTimeout
	}
}

func isTerminalRunStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

type benchmarkStepResult struct {
	RequestJSON   string
	ResponseJSON  string
	BenchmarkID   string
	ConfigID      string
	EngineVersion string
	EngineImage   string
	ResourceUsage map[string]any
	DeployConfig  map[string]any
	MatrixJSON    string
	TotalCells    int
	SuccessCells  int
}

type deploymentLease struct {
	Name    string
	Config  map[string]any
	Created bool
}

func meaningfulBenchmarkResult(summary map[string]any) bool {
	if summary == nil {
		return false
	}
	if primaryBenchmarkRate(summary) > 0 {
		return true
	}
	if reqs := readFloatField(summary, "successful_requests"); reqs > 0 {
		return true
	}
	if outputs := readFloatField(summary, "avg_output_tokens"); outputs > 0 {
		return true
	}
	return false
}

func (m *ExplorationManager) resolveCurrentDeployConfig(ctx context.Context, model, engine string) map[string]any {
	if m.tools == nil || strings.TrimSpace(model) == "" {
		return nil
	}
	statusArgs, _ := json.Marshal(map[string]string{"name": model})
	result, err := m.tools.ExecuteTool(ctx, "deploy.status", statusArgs)
	if err != nil || result == nil {
		return nil
	}
	var status struct {
		Ready   bool              `json:"ready"`
		Engine  string            `json:"engine"`
		Config  map[string]any    `json:"config"`
		Labels  map[string]string `json:"labels"`
		Runtime string            `json:"runtime"`
	}
	if err := json.Unmarshal([]byte(result.Content), &status); err != nil {
		return nil
	}
	if !status.Ready || len(status.Config) == 0 {
		return nil
	}
	if engine = strings.TrimSpace(engine); engine != "" {
		matchedEngine := strings.TrimSpace(status.Engine)
		if matchedEngine == "" && status.Labels != nil {
			matchedEngine = strings.TrimSpace(status.Labels["aima.dev/engine"])
		}
		if matchedEngine != "" && !strings.EqualFold(matchedEngine, engine) {
			return nil
		}
	}
	return cloneAnyMap(status.Config)
}

type deploymentStepResult struct {
	RequestJSON  string
	ResponseJSON string
	Address      string
	Endpoint     string
	Config       map[string]any
}

type ExplorationStart struct {
	Kind              string                        `json:"kind"`
	Goal              string                        `json:"goal"`
	PlanID            string                        `json:"plan_id,omitempty"` // D3: links run to explorer plan
	Target            ExplorationTarget             `json:"target"`
	Executor          string                        `json:"executor,omitempty"`
	RequestedBy       string                        `json:"requested_by,omitempty"`
	ApprovalMode      string                        `json:"approval_mode,omitempty"`
	SourceRef         string                        `json:"source_ref,omitempty"`
	EngineParams      map[string]any                `json:"engine_params,omitempty"`
	SearchSpace       map[string][]any              `json:"search_space,omitempty"`
	Constraints       ExplorationConstraints        `json:"constraints,omitempty"`
	BenchmarkProfiles []ExplorationBenchmarkProfile `json:"benchmark_profiles,omitempty"`
}

type ExplorationStatus struct {
	Run           *state.ExplorationRun     `json:"run"`
	Events        []*state.ExplorationEvent `json:"events,omitempty"`
	TuningSession *TuningSession            `json:"tuning_session,omitempty"`
}

type ExplorationManager struct {
	db    *state.DB
	tuner *Tuner
	tools ToolExecutor

	mu              sync.Mutex
	activeRuns      map[string]context.CancelFunc
	activeProbeInfo map[string]string // model name → health check path from engine YAML
	tuneRunID       string
}

func NewExplorationManager(db *state.DB, tuner *Tuner, tools ToolExecutor) *ExplorationManager {
	return &ExplorationManager{
		db:              db,
		tuner:           tuner,
		tools:           tools,
		activeRuns:      make(map[string]context.CancelFunc),
		activeProbeInfo: make(map[string]string),
	}
}

func (m *ExplorationManager) ActiveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.activeRuns)
}

func (m *ExplorationManager) Start(ctx context.Context, req ExplorationStart) (*state.ExplorationRun, error) {
	if m.db == nil {
		return nil, fmt.Errorf("exploration manager requires state DB")
	}

	run, err := m.newRun(ctx, req)
	if err != nil {
		return nil, err
	}
	if run.Kind == "tune" && m.tuner == nil {
		return nil, fmt.Errorf("exploration manager requires tuner")
	}
	if (run.Kind == "validate" || run.Kind == "open_question") && m.tools == nil {
		return nil, fmt.Errorf("exploration manager requires tool executor")
	}

	m.mu.Lock()
	if run.Kind == "tune" && m.tuneRunID != "" {
		m.mu.Unlock()
		return nil, fmt.Errorf("exploration run %s already tuning", m.tuneRunID)
	}
	if err := m.db.InsertExplorationRun(ctx, run); err != nil {
		m.mu.Unlock()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	m.activeRuns[run.ID] = cancel
	if req.Target.HealthCheckPath != "" {
		m.activeProbeInfo[run.ModelID] = req.Target.HealthCheckPath
	}
	if run.Kind == "tune" {
		m.tuneRunID = run.ID
	}
	m.mu.Unlock()

	go m.execute(runCtx, run)
	return run, nil
}

// StartAndWait starts an exploration run and blocks until it reaches a terminal state.
func (m *ExplorationManager) StartAndWait(ctx context.Context, req ExplorationStart) (*ExplorationStatus, error) {
	run, err := m.Start(ctx, req)
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var timeout <-chan time.Time
	var timeoutTimer *time.Timer
	timeoutLabel := time.Duration(0)
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeoutLabel = startAndWaitFallbackTimeout(req.Kind)
		timeoutTimer = time.NewTimer(timeoutLabel)
		timeout = timeoutTimer.C
		defer timeoutTimer.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.Background(), startAndWaitStopCleanupTimeout)
			_, _ = m.stopAndWaitForTerminal(stopCtx, run.ID)
			cancel()
			return nil, fmt.Errorf("exploration %s canceled: %w", run.ID, ctx.Err())
		case <-timeout:
			stopCtx, cancel := context.WithTimeout(context.Background(), startAndWaitStopCleanupTimeout)
			status, waitErr := m.stopAndWaitForTerminal(stopCtx, run.ID)
			cancel()
			timeoutErr := fmt.Errorf("exploration %s timed out after %s", run.ID, timeoutLabel)
			if waitErr == nil || errors.Is(waitErr, context.DeadlineExceeded) || errors.Is(waitErr, context.Canceled) {
				return status, timeoutErr
			}
			return status, fmt.Errorf("%w (stop wait: %v)", timeoutErr, waitErr)
		case <-ticker.C:
			status, err := m.Status(ctx, run.ID)
			if err != nil {
				return nil, err
			}
			if isTerminalRunStatus(status.Run.Status) && !m.isRunActive(run.ID) {
				return status, nil
			}
		}
	}
}

func (m *ExplorationManager) finishRunWithError(run *state.ExplorationRun, err error) {
	if run == nil || err == nil {
		return
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		run.Status = "cancelled"
	} else {
		run.Status = "failed"
	}
	run.Error = err.Error()
	run.CompletedAt = time.Now()
	_ = m.db.UpdateExplorationRun(context.Background(), run)
}

func (m *ExplorationManager) finishRunIfContextDone(ctx context.Context, run *state.ExplorationRun) bool {
	if ctx == nil {
		return false
	}
	if err := ctx.Err(); err != nil {
		m.finishRunWithError(run, err)
		return true
	}
	return false
}

func (m *ExplorationManager) Stop(ctx context.Context, runID string) (*ExplorationStatus, error) {
	run, err := m.db.GetExplorationRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	cancel, ok := m.activeRuns[runID]
	m.mu.Unlock()
	if ok {
		cancel()
		if run.Kind == "tune" {
			m.tuner.Stop()
		}
	}
	return m.Status(ctx, runID)
}

func (m *ExplorationManager) Status(ctx context.Context, runID string) (*ExplorationStatus, error) {
	run, err := m.db.GetExplorationRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	events, err := m.db.ListExplorationEvents(ctx, runID)
	if err != nil {
		return nil, err
	}

	status := &ExplorationStatus{
		Run:    run,
		Events: events,
	}

	m.mu.Lock()
	activeTune := m.tuneRunID == runID
	m.mu.Unlock()
	if activeTune {
		status.TuningSession = m.tuner.CurrentSession()
	}
	return status, nil
}

func (m *ExplorationManager) Result(ctx context.Context, runID string) (*ExplorationStatus, error) {
	return m.Status(ctx, runID)
}

func (m *ExplorationManager) isRunActive(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.activeRuns[runID]
	return ok
}

func (m *ExplorationManager) waitForTerminalStatus(ctx context.Context, runID string) (*ExplorationStatus, error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := m.Status(ctx, runID)
		if err != nil {
			return nil, err
		}
		if isTerminalRunStatus(status.Run.Status) && !m.isRunActive(runID) {
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *ExplorationManager) stopAndWaitForTerminal(ctx context.Context, runID string) (*ExplorationStatus, error) {
	status, err := m.Stop(ctx, runID)
	if err != nil {
		return nil, err
	}
	if status != nil && isTerminalRunStatus(status.Run.Status) {
		return status, nil
	}
	return m.waitForTerminalStatus(ctx, runID)
}

func (m *ExplorationManager) execute(ctx context.Context, run *state.ExplorationRun) {
	defer m.cleanup(run.ID, run.Kind, run.ModelID)

	switch run.Kind {
	case "tune":
		m.executeTune(ctx, run)
	case "validate":
		m.executeValidate(ctx, run)
	case "open_question":
		m.executeOpenQuestion(ctx, run)
	default:
		run.Status = "failed"
		run.Error = fmt.Sprintf("exploration kind %q not implemented", run.Kind)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
	}
}

func (m *ExplorationManager) executeTune(ctx context.Context, run *state.ExplorationRun) {
	var plan ExplorationPlan
	if err := json.Unmarshal([]byte(run.PlanJSON), &plan); err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("parse exploration plan: %v", err)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	run.Status = "running"
	run.StartedAt = time.Now()
	_ = m.db.UpdateExplorationRun(context.Background(), run)

	bp := firstProfile(plan.BenchmarkProfiles)
	bp = tuneBenchmarkProfile(bp)
	tuningConfig := TuningConfig{
		Model:       plan.Target.Model,
		Hardware:    plan.Target.Hardware,
		Engine:      plan.Target.Engine,
		Endpoint:    bp.Endpoint,
		Parameters:  buildTuningParams(plan.SearchSpace),
		Concurrency: bp.Concurrency,
		NumRequests: bp.RequestsPerCombo,
		InputTokens: bp.InputTokens,
		MaxTokens:   bp.MaxTokens,
		Rounds:      bp.Rounds,
		Modality:    benchmarkModality(plan.Target.ModelType),
		MaxConfigs:  plan.Constraints.MaxCandidates,
		// Bug-8: explorer runs task-boundary teardown, so skip the tuner's
		// final best-config redeploy to keep the GPU cold between tasks.
		CleanupAfter: true,
	}

	requestJSON, _ := json.Marshal(map[string]any{
		"action":       "start",
		"model":        tuningConfig.Model,
		"hardware":     tuningConfig.Hardware,
		"engine":       tuningConfig.Engine,
		"endpoint":     tuningConfig.Endpoint,
		"parameters":   tuningConfig.Parameters,
		"concurrency":  tuningConfig.Concurrency,
		"num_requests": tuningConfig.NumRequests,
		"input_tokens": tuningConfig.InputTokens,
		"max_tokens":   tuningConfig.MaxTokens,
		"rounds":       tuningConfig.Rounds,
		"modality":     tuningConfig.Modality,
		"max_configs":  tuningConfig.MaxConfigs,
	})
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:       run.ID,
		StepIndex:   0,
		StepKind:    "tune",
		Status:      "running",
		ToolName:    "tuning",
		RequestJSON: string(requestJSON),
	})

	session, err := m.tuner.Start(ctx, tuningConfig)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		responseJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
			RunID:        run.ID,
			StepIndex:    0,
			StepKind:     "tune",
			Status:       "failed",
			ToolName:     "tuning",
			ResponseJSON: string(responseJSON),
		})
		return
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		current := m.tuner.CurrentSession()
		if current == nil {
			run.Status = "failed"
			run.Error = "tuning session disappeared"
			run.CompletedAt = time.Now()
			_ = m.db.UpdateExplorationRun(context.Background(), run)
			return
		}

		summaryJSON, _ := json.Marshal(summarizeTuningSession(current))
		run.SummaryJSON = string(summaryJSON)

		switch current.Status {
		case "running":
			_ = m.db.UpdateExplorationRun(context.Background(), run)
		case "completed":
			run.Status = "completed"
			run.CompletedAt = time.Now()
			_ = m.db.UpdateExplorationRun(context.Background(), run)
			_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
				RunID:        run.ID,
				StepIndex:    0,
				StepKind:     "tune",
				Status:       "completed",
				ToolName:     "tuning",
				ResponseJSON: string(summaryJSON),
				ArtifactType: "tuning_session",
				ArtifactID:   session.ID,
			})
			return
		case "cancelled":
			run.Status = "cancelled"
			run.CompletedAt = time.Now()
			_ = m.db.UpdateExplorationRun(context.Background(), run)
			_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
				RunID:        run.ID,
				StepIndex:    0,
				StepKind:     "tune",
				Status:       "cancelled",
				ToolName:     "tuning",
				ResponseJSON: string(summaryJSON),
				ArtifactType: "tuning_session",
				ArtifactID:   session.ID,
			})
			return
		case "failed":
			run.Status = "failed"
			run.Error = current.Error
			run.CompletedAt = time.Now()
			_ = m.db.UpdateExplorationRun(context.Background(), run)
			_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
				RunID:        run.ID,
				StepIndex:    0,
				StepKind:     "tune",
				Status:       "failed",
				ToolName:     "tuning",
				ResponseJSON: string(summaryJSON),
				ArtifactType: "tuning_session",
				ArtifactID:   session.ID,
			})
			return
		}

		select {
		case <-ctx.Done():
			m.tuner.Stop()
		case <-ticker.C:
		}
	}
}

func tuneBenchmarkProfile(profile ExplorationBenchmarkProfile) ExplorationBenchmarkProfile {
	tuned := profile
	tuned.Concurrency = firstPositiveInt(profile.Concurrency, firstPositiveTokenLevel(profile.ConcurrencyLevels))
	tuned.InputTokens = firstPositiveInt(profile.InputTokens, firstPositiveTokenLevel(profile.InputTokenLevels))
	tuned.MaxTokens = firstPositiveInt(profile.MaxTokens, firstPositiveTokenLevel(profile.MaxTokenLevels))
	if tuned.Rounds <= 0 {
		tuned.Rounds = 1
	}
	if tuned.RequestsPerCombo <= 0 {
		tuned.RequestsPerCombo = 10
	}
	return tuned
}

func firstPositiveTokenLevel(values []int) int {
	for _, value := range normalizeTokenLevels(values) {
		if value > 0 {
			return value
		}
	}
	return 0
}

func summarizeTuningSession(session *TuningSession) map[string]any {
	payload := map[string]any{
		"tuning_session": session,
	}
	if session == nil || len(session.Results) == 0 {
		return payload
	}

	best := tuningBestResult(session)
	payload["benchmark_id"] = best.BenchmarkID
	payload["config_id"] = best.ConfigID
	payload["engine_version"] = best.EngineVersion
	payload["engine_image"] = best.EngineImage
	if cfg := cloneAnyMap(session.BestConfig); len(cfg) > 0 {
		payload["deploy_config"] = cfg
	}
	if usage := cloneAnyMap(best.ResourceUsage); len(usage) > 0 {
		payload["resource_usage"] = usage
	}
	payload["result"] = map[string]any{
		"throughput_tps": best.ThroughputTPS,
		"latency_p50_ms": best.LatencyP50Ms,
		"ttft_p50_ms":    best.TTFTP50Ms,
		"ttft_p95_ms":    best.TTFTP95Ms,
		"tpot_p50_ms":    best.TPOTP50Ms,
		"tpot_p95_ms":    best.TPOTP95Ms,
		"qps":            best.QPS,
		"config": map[string]any{
			"concurrency":  best.Concurrency,
			"num_requests": best.NumRequests,
			"warmup_count": best.WarmupCount,
			"rounds":       best.Rounds,
			"input_tokens": best.InputTokens,
			"max_tokens":   best.MaxTokens,
		},
	}
	payload["benchmark_profile"] = map[string]any{
		"concurrency":       best.Concurrency,
		"num_requests":      best.NumRequests,
		"warmup_count":      best.WarmupCount,
		"rounds":            best.Rounds,
		"input_tokens":      best.InputTokens,
		"max_tokens":        best.MaxTokens,
		"avg_input_tokens":  best.AvgInputTokens,
		"avg_output_tokens": best.AvgOutputTokens,
	}
	payload["matrix_profiles"] = tuningMatrixProfiles(session.Results)
	// total_cells = planned cells; success_cells = cells that produced a
	// usable benchmark row. Keeping them distinct lets the partial-preserve
	// branch tell "2 attempted, 1 succeeded" apart from "1 of 1 succeeded".
	payload["total_cells"] = session.Total
	payload["success_cells"] = len(session.Results)
	return payload
}

func tuningBestResult(session *TuningSession) TuningResult {
	best := session.Results[0]
	for _, result := range session.Results[1:] {
		if result.Score > best.Score {
			best = result
			continue
		}
		if sameConfigMap(session.BestConfig, result.ConfigOverrides) {
			best = result
		}
	}
	return best
}

func tuningMatrixProfiles(results []TuningResult) []map[string]any {
	profiles := make([]map[string]any, 0, len(results))
	for _, result := range results {
		cell := map[string]any{
			"concurrency":  result.Concurrency,
			"input_tokens": result.InputTokens,
			"max_tokens":   result.MaxTokens,
			"result": map[string]any{
				"throughput_tps": result.ThroughputTPS,
				"latency_p50_ms": result.LatencyP50Ms,
				"ttft_p50_ms":    result.TTFTP50Ms,
				"ttft_p95_ms":    result.TTFTP95Ms,
				"tpot_p50_ms":    result.TPOTP50Ms,
				"tpot_p95_ms":    result.TPOTP95Ms,
				"qps":            result.QPS,
			},
		}
		if result.BenchmarkID != "" {
			cell["benchmark_id"] = result.BenchmarkID
		}
		if result.ConfigID != "" {
			cell["config_id"] = result.ConfigID
		}
		if result.EngineVersion != "" {
			cell["engine_version"] = result.EngineVersion
		}
		if result.EngineImage != "" {
			cell["engine_image"] = result.EngineImage
		}
		if usage := cloneAnyMap(result.ResourceUsage); len(usage) > 0 {
			cell["resource_usage"] = usage
		}
		profiles = append(profiles, map[string]any{
			"label": tuningResultLabel(result.ConfigOverrides),
			"cells": []map[string]any{cell},
		})
	}
	return profiles
}

func tuningResultLabel(config map[string]any) string {
	if len(config) == 0 {
		return "candidate"
	}
	keys := make([]string, 0, len(config))
	for key := range config {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, config[key]))
	}
	return strings.Join(parts, ", ")
}

func (m *ExplorationManager) executeValidate(ctx context.Context, run *state.ExplorationRun) {
	if m.tools == nil {
		run.Status = "failed"
		run.Error = "exploration validate requires tool executor"
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	var plan ExplorationPlan
	if err := json.Unmarshal([]byte(run.PlanJSON), &plan); err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("parse exploration plan: %v", err)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	run.Status = "running"
	run.StartedAt = time.Now()
	_ = m.db.UpdateExplorationRun(context.Background(), run)

	// Pre-flight: ensure the model is deployed before benchmarking.
	// Without this, benchmark.run hits an empty endpoint and gets 0 tok/s.
	lease, err := m.ensureDeployed(ctx, run, plan)
	if err != nil {
		m.finishRunWithError(run, fmt.Errorf("pre-flight deploy: %w", err))
		return
	}
	if lease != nil {
		defer m.releaseDeployment(lease)
	}
	if m.finishRunIfContextDone(ctx, run) {
		return
	}

	// Resolve actual endpoint from deploy.status — the model may be on a non-default port.
	if firstProfile(plan.BenchmarkProfiles).Endpoint == "" {
		if addr := m.resolveDeployEndpoint(ctx, plan.Target.Model); addr != "" {
			for i := range plan.BenchmarkProfiles {
				plan.BenchmarkProfiles[i].Endpoint = addr
			}
			slog.Info("exploration: resolved benchmark endpoint from deployment",
				"model", plan.Target.Model, "endpoint", addr)
		}
	}

	stepResult, err := m.executeBenchmarkStep(ctx, run, plan, "validate", 0)
	if err != nil {
		if stepResult != nil {
			run.SummaryJSON = stepResult.ResponseJSON
		}
		m.finishRunWithError(run, err)
		return
	}
	if m.finishRunIfContextDone(ctx, run) {
		return
	}

	run.Status = "completed"
	run.Error = ""
	run.CompletedAt = time.Now()
	run.SummaryJSON = stepResult.ResponseJSON
	_ = m.db.UpdateExplorationRun(context.Background(), run)

	// Task #13: After successful benchmark, write discovered knowledge as overlay YAML.
	var deployCfg map[string]any
	if lease != nil {
		deployCfg = lease.Config
	}
	m.maybeCreateKnowledge(ctx, run, plan, stepResult, deployCfg)
}

// ensureDeployed deploys the target model+engine with config if not already running.
// Returns the resolved deployment config (for overlay YAML creation).
// B17: checks deploy.status first — if already ready, skip deploy; if starting, wait;
// only call deploy.apply when no existing deployment or previous one failed.
func (m *ExplorationManager) ensureDeployed(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan) (*deploymentLease, error) {
	if m.tools == nil {
		return nil, fmt.Errorf("no tool executor")
	}

	// B17: Pre-check — avoid "already starting" errors by inspecting current state.
	ds := m.checkDeployStatus(ctx, plan.Target.Model)

	// Engine-aware check: if a deployment exists but uses a DIFFERENT engine
	// than what we want, we must tear it down and redeploy on the target engine.
	// Without this, explorer would skip deploy and benchmark against the wrong endpoint.
	engineMatch := ds.Engine == "" || plan.Target.Engine == "" ||
		strings.EqualFold(ds.Engine, plan.Target.Engine)

	if ds.Ready && engineMatch {
		if err := m.waitForInferenceReady(ctx, plan.Target.Model); err != nil {
			return nil, fmt.Errorf("verify ready deployment %s: %w", plan.Target.Model, err)
		}
		slog.Info("exploration: model already deployed, skipping deploy",
			"model", plan.Target.Model, "engine", plan.Target.Engine)
		return &deploymentLease{
			Name:    plan.Target.Model,
			Config:  m.resolveCurrentDeployConfig(ctx, plan.Target.Model, plan.Target.Engine),
			Created: false,
		}, nil
	}
	if ds.Ready && !engineMatch {
		// Wrong engine deployed. Delete the existing deployment so we can redeploy
		// on the target engine. This is safe because the explorer is the only actor
		// that would have deployed a different engine for the same model.
		slog.Info("exploration: engine mismatch — deleting existing deployment before redeploy",
			"model", plan.Target.Model, "deployed_engine", ds.Engine, "target_engine", plan.Target.Engine)
		deleteArgs, _ := json.Marshal(map[string]string{"name": plan.Target.Model})
		if _, err := m.tools.ExecuteTool(ctx, "deploy.delete", deleteArgs); err != nil {
			slog.Warn("exploration: failed to delete mismatched deployment, proceeding with deploy.apply",
				"model", plan.Target.Model, "error", err)
		} else {
			m.waitForDeleteComplete(ctx, plan.Target.Model)
		}
	}
	if (ds.Phase == "starting" || ds.Phase == "pulling") && engineMatch {
		slog.Info("exploration: model already deploying, waiting for ready",
			"model", plan.Target.Model, "phase", ds.Phase)
		if err := m.waitForReady(ctx, plan.Target.Model); err != nil {
			return nil, fmt.Errorf("wait for in-progress deploy %s: %w", plan.Target.Model, err)
		}
		if err := m.waitForInferenceReady(ctx, plan.Target.Model); err != nil {
			return nil, fmt.Errorf("wait for in-progress deployment endpoint %s: %w", plan.Target.Model, err)
		}
		return &deploymentLease{
			Name:    plan.Target.Model,
			Config:  m.resolveCurrentDeployConfig(ctx, plan.Target.Model, plan.Target.Engine),
			Created: false,
		}, nil
	}

	// Native runtime is exclusive. Only same-runtime (native) deployments are
	// true conflicts — Docker/K3S containers use separate resource spaces.
	if plan.Target.Runtime == "native" {
		conflicts := m.activeConflictingDeploys(ctx, plan.Target.Model, "native")
		if len(conflicts) > 0 {
			return nil, fmt.Errorf("native runtime busy: active deployments %s; explorer will not delete them automatically", strings.Join(conflicts, ", "))
		}
	}

	args := map[string]any{
		"model":     plan.Target.Model,
		"engine":    plan.Target.Engine,
		"auto_pull": false, // Explorer must never download — only use locally available resources.
	}
	// Merge planner-authored engine_params and SearchSpace into deploy config.
	// SearchSpace entries win over EngineParams for the same key: tune tasks
	// already flatten their engine_params into SearchSpace upstream, so the
	// overlap is deliberate and search_space always represents the resolved
	// point to probe.
	if len(plan.EngineParams) > 0 || len(plan.SearchSpace) > 0 {
		config := make(map[string]any, len(plan.EngineParams)+len(plan.SearchSpace))
		for k, v := range plan.EngineParams {
			if v == nil {
				continue
			}
			config[k] = v
		}
		for k, vals := range plan.SearchSpace {
			if len(vals) > 0 {
				config[k] = vals[0]
			}
		}
		if len(config) > 0 {
			args["config"] = config
		}
	}
	deployArgs, _ := json.Marshal(args)

	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:       run.ID,
		StepIndex:   0,
		StepKind:    "deploy",
		Status:      "running",
		ToolName:    "deploy.apply",
		RequestJSON: string(deployArgs),
	})

	result, err := m.tools.ExecuteTool(ctx, "deploy.apply", deployArgs)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		responseJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
			RunID:        run.ID,
			StepIndex:    0,
			StepKind:     "deploy",
			Status:       "failed",
			ToolName:     "deploy.apply",
			RequestJSON:  string(deployArgs),
			ResponseJSON: string(responseJSON),
		})
		return nil, fmt.Errorf("deploy %s on %s: %w", plan.Target.Model, plan.Target.Engine, err)
	}

	responseJSON := ""
	if result != nil {
		responseJSON = result.Content
	}
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:        run.ID,
		StepIndex:    0,
		StepKind:     "deploy",
		Status:       "completed",
		ToolName:     "deploy.apply",
		ResponseJSON: responseJSON,
	})

	// Extract resolved config from deploy.apply response for overlay YAML.
	deployCfg := parseDeployConfig(responseJSON)
	lease := &deploymentLease{
		Name:    plan.Target.Model,
		Config:  deployCfg,
		Created: true,
	}

	slog.Info("exploration: model deployed for validation",
		"model", plan.Target.Model, "engine", plan.Target.Engine)

	// B14: Wait for the service to become ready before benchmarking.
	if err := m.waitForReady(ctx, plan.Target.Model); err != nil {
		m.releaseDeployment(lease)
		return nil, fmt.Errorf("wait for deployed service %s: %w", plan.Target.Model, err)
	}
	if err := m.waitForInferenceReady(ctx, plan.Target.Model); err != nil {
		m.releaseDeployment(lease)
		return nil, fmt.Errorf("wait for deployed endpoint %s: %w", plan.Target.Model, err)
	}
	if len(lease.Config) == 0 {
		lease.Config = m.resolveCurrentDeployConfig(ctx, plan.Target.Model, plan.Target.Engine)
	}
	return lease, nil
}

// deployStatusResult holds the parsed deploy.status response.
type deployStatusResult struct {
	Phase   string
	Ready   bool
	Engine  string
	Runtime string
}

// checkDeployStatus returns the current phase, readiness, engine, and runtime of a deployment.
// Returns zero-value deployStatusResult if the deployment doesn't exist or status can't be determined.
func (m *ExplorationManager) checkDeployStatus(ctx context.Context, model string) deployStatusResult {
	statusArgs, _ := json.Marshal(map[string]string{"name": model})
	result, err := m.tools.ExecuteTool(ctx, "deploy.status", statusArgs)
	if err != nil || result == nil {
		return deployStatusResult{}
	}
	var status struct {
		Phase   string `json:"phase"`
		Ready   bool   `json:"ready"`
		Engine  string `json:"engine"`
		Runtime string `json:"runtime"`
	}
	if json.Unmarshal([]byte(result.Content), &status) != nil {
		return deployStatusResult{}
	}
	return deployStatusResult{
		Phase:   status.Phase,
		Ready:   status.Ready,
		Engine:  status.Engine,
		Runtime: status.Runtime,
	}
}

// activeConflictingDeploys returns active deployments other than the target model
// that run on the same runtime. Docker containers don't conflict with native
// processes — they use separate resource spaces. When targetRuntime is empty,
// all active deployments are considered conflicts (legacy behavior).
func (m *ExplorationManager) activeConflictingDeploys(ctx context.Context, targetModel, targetRuntime string) []string {
	listResult, err := m.tools.ExecuteTool(ctx, "deploy.list", []byte("{}"))
	if err != nil || listResult == nil {
		return nil
	}
	var deploys []struct {
		Name    string `json:"name"`
		Phase   string `json:"phase"`
		Runtime string `json:"runtime"`
	}
	if json.Unmarshal([]byte(listResult.Content), &deploys) != nil {
		return nil
	}
	var conflicts []string
	for _, d := range deploys {
		if strings.EqualFold(d.Name, targetModel) {
			continue
		}
		// Only running/starting deployments actively hold resources.
		if d.Phase != "running" && d.Phase != "starting" {
			continue
		}
		// Runtime-aware: only same-runtime deployments are true conflicts.
		// e.g., Docker vllm containers don't block native sglang-kt processes.
		if targetRuntime != "" && d.Runtime != "" && !strings.EqualFold(d.Runtime, targetRuntime) {
			continue
		}
		conflicts = append(conflicts, d.Name)
	}
	return conflicts
}

// waitForDeleteComplete polls deploy.status until the deployment is no longer running.
func (m *ExplorationManager) waitForDeleteComplete(ctx context.Context, name string) {
	waitForGPURelease(ctx, m.tools, name, gpuReleaseGrace)
}

func (m *ExplorationManager) releaseDeployment(lease *deploymentLease) {
	if lease == nil || !lease.Created || m.tools == nil || strings.TrimSpace(lease.Name) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	deleteArgs, _ := json.Marshal(map[string]string{"name": lease.Name})
	result, err := m.tools.ExecuteTool(ctx, "deploy.delete", deleteArgs)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		slog.Warn("exploration: cleanup deployment failed", "model", lease.Name, "error", err)
		return
	}
	m.waitForDeleteComplete(ctx, lease.Name)
	slog.Info("exploration: cleaned up owned deployment", "model", lease.Name)
}

// waitForGPURelease polls deploy.status until the named deployment is no longer active,
// then sleeps for gracePeriod to let the GPU driver fully reclaim memory.
// Shared by ExplorationManager and Tuner.
func waitForGPURelease(ctx context.Context, tools ToolExecutor, name string, gracePeriod time.Duration) {
	const (
		pollInterval = 2 * time.Second
		maxWait      = 30 * time.Second
	)
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		phase := checkDeployPhase(ctx, tools, name)
		// Use negative logic: only keep waiting if the process is still active.
		if phase != "running" && phase != "starting" && phase != "pulling" {
			slog.Info("waitForGPURelease: deployment no longer holding GPU", "name", name, "phase", phase)
			if gracePeriod > 0 {
				time.Sleep(gracePeriod)
			}
			return
		}
		slog.Info("waitForGPURelease: waiting for deployment to release GPU",
			"name", name, "phase", phase)
		select {
		case <-ctx.Done():
			return
		case <-time.After(pollInterval):
		}
	}
	slog.Warn("waitForGPURelease: timeout waiting for deployment to release GPU, proceeding anyway",
		"name", name, "waited", maxWait)
}

// checkDeployPhase returns the current phase of a deployment, or "" if unknown/gone.
func checkDeployPhase(ctx context.Context, tools ToolExecutor, name string) string {
	statusArgs, _ := json.Marshal(map[string]string{"name": name})
	result, err := tools.ExecuteTool(ctx, "deploy.status", statusArgs)
	if err != nil || result == nil {
		return ""
	}
	var status struct {
		Phase string `json:"phase"`
	}
	if json.Unmarshal([]byte(result.Content), &status) != nil {
		return ""
	}
	return status.Phase
}

// waitForReady polls deploy.status until the deployment reports ready.
// Uses progress-based stall detection instead of a fixed timeout.
// Safety net = max(EstimatedTotalS * 3, 15min) prevents infinite wait
// when an engine has no log_patterns.
//
// deployVanishGrace tolerates transient "not found" responses (runtime
// registration race after deploy.apply) before concluding the deployment
// has truly disappeared; without this grace a slow runtime would spin the
// full safety-net window before failing.
func (m *ExplorationManager) waitForReady(ctx context.Context, model string) error {
	if m.tools == nil {
		return nil
	}
	const (
		pollInterval      = 5 * time.Second
		deployVanishGrace = 2
	)

	// First poll to get EstimatedTotalS for safety net calculation
	safetyNet := 15 * time.Minute
	deadline := time.Now().Add(safetyNet)

	everSeenAlive := false
	consecutiveMissing := 0

	for time.Now().Before(deadline) {
		statusArgs, _ := json.Marshal(map[string]string{"name": model})
		result, err := m.tools.ExecuteTool(ctx, "deploy.status", statusArgs)
		if err != nil {
			// A "not found" response means the runtime no longer knows about
			// this deployment. If the deploy was alive earlier in the wait
			// loop, treat the first miss as authoritative — it vanished. If
			// we have not yet seen it alive, allow deployVanishGrace polls
			// before giving up so we don't race the runtime's registration.
			if isDeploymentNotFound(err) {
				consecutiveMissing++
				threshold := deployVanishGrace
				if everSeenAlive {
					threshold = 1
				}
				if consecutiveMissing >= threshold {
					return fmt.Errorf("deployment %s disappeared before becoming ready: %w", model, err)
				}
			}
			// Other transient errors: keep polling.
		} else if result != nil {
			var status struct {
				Phase           string `json:"phase"`
				Ready           bool   `json:"ready"`
				Stalled         bool   `json:"stalled"`
				StartupProgress int    `json:"startup_progress"`
				StartupPhase    string `json:"startup_phase"`
				StartupMessage  string `json:"startup_message"`
				EstimatedTotalS int    `json:"estimated_total_s"`
				ErrorLines      string `json:"error_lines"`
			}
			if json.Unmarshal([]byte(result.Content), &status) == nil {
				everSeenAlive = true
				consecutiveMissing = 0

				// Adjust safety net on first EstimatedTotalS reading
				if status.EstimatedTotalS > 0 {
					dynamic := time.Duration(status.EstimatedTotalS*3) * time.Second
					if dynamic > safetyNet {
						deadline = time.Now().Add(dynamic)
						safetyNet = dynamic
					}
				}

				if status.Ready {
					slog.Info("exploration: service ready", "model", model)
					return nil
				}

				// Fast fail on terminal phases
				switch status.Phase {
				case "failed", "stopped", "error", "exited":
					if detail := summarizeDiagnosticErrorLines(status.ErrorLines); detail != "" {
						return fmt.Errorf("deployment %s entered terminal phase %q: %s", model, status.Phase, detail)
					}
					if detail := strings.TrimSpace(status.StartupMessage); detail != "" {
						return fmt.Errorf("deployment %s entered terminal phase %q: %s", model, status.Phase, detail)
					}
					return fmt.Errorf("deployment %s entered terminal phase %q", model, status.Phase)
				}

				// Stall detection: runtime layer says no progress
				if status.Stalled {
					return fmt.Errorf("deployment %s stalled at %s (%d%%)", model, status.StartupPhase, status.StartupProgress)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("timeout waiting for %s to become ready (safety net %v)", model, safetyNet)
}

// isDeploymentNotFound is true when an error from deploy.status / a runtime's
// Status() signals that the deployment is unknown to every runtime. Keeping
// detection substring-based tolerates variation in error wrapping across the
// docker/k3s/native runtimes without requiring a shared error type.
func isDeploymentNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "no such container") || strings.Contains(msg, "no matching deployment")
}

// resolveDeployEndpoint queries deploy.status to get the actual inference base address.
func (m *ExplorationManager) resolveDeployEndpoint(ctx context.Context, model string) string {
	statusArgs, _ := json.Marshal(map[string]string{"name": model})
	result, err := m.tools.ExecuteTool(ctx, "deploy.status", statusArgs)
	if err != nil || result == nil {
		return ""
	}
	var status struct {
		Address string `json:"address"`
		Ready   bool   `json:"ready"`
	}
	if json.Unmarshal([]byte(result.Content), &status) != nil || status.Address == "" {
		return ""
	}
	return fmt.Sprintf("http://%s", status.Address)
}

// probeInferenceEndpoint verifies the inference engine is actually serving.
// Uses the engine YAML's health_check.path when available; falls back to
// /v1/models (the standard OpenAI endpoint) for engines without explicit config.
func (m *ExplorationManager) probeInferenceEndpoint(ctx context.Context, model string) bool {
	addr := m.resolveDeployEndpoint(ctx, model)
	if addr == "" {
		return false
	}
	baseURL := strings.TrimRight(addr, "/")
	probePath := "/v1/models"
	if hp := m.healthCheckPath(model); hp != "" {
		probePath = hp
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+probePath, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// healthCheckPath returns the engine YAML health_check.path for a running exploration.
func (m *ExplorationManager) healthCheckPath(model string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeProbeInfo[model]
}

// waitForInferenceReady polls the inference endpoint until it actually serves
// requests. This catches the gap where deploy.status reports ready=true but the
// engine is still loading weights or running health checks.
func (m *ExplorationManager) waitForInferenceReady(ctx context.Context, model string) error {
	const pollInterval = 5 * time.Second

	// Fast path: already serving.
	if m.probeInferenceEndpoint(ctx, model) {
		return nil
	}

	// Use remaining context deadline if available, otherwise default to 10 minutes.
	// The caller (executeValidate) may have already consumed time in waitForReady(),
	// so the context deadline naturally shrinks for larger models.
	timeout := 10 * time.Minute
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining > pollInterval {
			timeout = remaining
		} else {
			return fmt.Errorf("timeout waiting for inference endpoint %s (context deadline too close: %v)", model, remaining)
		}
	}

	slog.Info("exploration: inference not yet serving, waiting for endpoint",
		"model", model, "timeout", timeout)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
		if m.probeInferenceEndpoint(ctx, model) {
			slog.Info("exploration: inference endpoint now serving", "model", model)
			return nil
		}
	}
	if detail := m.summarizeInferenceReadinessFailure(ctx, model); detail != "" {
		return fmt.Errorf("timeout waiting for inference endpoint %s (%v): %s", model, timeout, detail)
	}
	return fmt.Errorf("timeout waiting for inference endpoint %s (%v)", model, timeout)
}

func (m *ExplorationManager) summarizeInferenceReadinessFailure(ctx context.Context, model string) string {
	if m.tools == nil || strings.TrimSpace(model) == "" {
		return ""
	}
	logsArgs, _ := json.Marshal(map[string]any{"name": model, "tail": 120})
	result, err := m.tools.ExecuteTool(ctx, "deploy.logs", logsArgs)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil || result == nil {
		return ""
	}
	return summarizeDiagnosticErrorLines(result.Content)
}

func summarizeDiagnosticErrorLines(errorLines string) string {
	lines := strings.Split(errorLines, "\n")
	bestLine := ""
	bestScore := 0
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		score := diagnosticLineScore(trimmed)
		if score > bestScore {
			bestLine = trimmed
			bestScore = score
		}
	}
	return bestLine
}

func diagnosticLineScore(line string) int {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case lower == "":
		return 0
	case strings.HasPrefix(lower, "error in cpuinfo:"):
		return 0
	case strings.Contains(lower, "outofmemoryerror"), strings.Contains(lower, "out of memory"):
		return 130
	case strings.Contains(lower, "keyerror:"),
		strings.Contains(lower, "valueerror:"),
		strings.Contains(lower, "assertionerror:"),
		strings.Contains(lower, "typeerror:"),
		strings.Contains(lower, "indexerror:"),
		strings.Contains(lower, "filenotfounderror:"),
		strings.Contains(lower, "modulenotfounderror:"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "no such file"),
		strings.Contains(lower, "not found"):
		return 120
	case strings.Contains(lower, "error:"),
		strings.Contains(lower, "exception"),
		strings.Contains(lower, "failed"),
		strings.Contains(lower, "cannot"),
		strings.Contains(lower, "panic"):
		return 80
	default:
		return 10
	}
}

// maybeCreateKnowledge writes a model YAML overlay when Explorer successfully
// benchmarks a model+engine combo. deployCfg is the resolved config from deploy.apply.
// This is the core value of autonomous exploration:
// discovered working configs become permanent catalog knowledge for future resolves.
func (m *ExplorationManager) maybeCreateKnowledge(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan, result *benchmarkStepResult, deployCfg map[string]any) {
	if m.tools == nil || result == nil {
		return
	}

	// Parse full benchmark result — includes all performance dimensions.
	var envelope struct {
		Result struct {
			ThroughputTPS   float64 `json:"throughput_tps"`
			QPS             float64 `json:"qps"`
			TTFTP50ms       float64 `json:"ttft_p50_ms"`
			TTFTP95ms       float64 `json:"ttft_p95_ms"`
			TTFTP99ms       float64 `json:"ttft_p99_ms"`
			TPOTP50ms       float64 `json:"tpot_p50_ms"`
			TPOTP95ms       float64 `json:"tpot_p95_ms"`
			ErrorRate       float64 `json:"error_rate"`
			TotalRequests   int     `json:"total_requests"`
			SuccessfulReqs  int     `json:"successful_requests"`
			AvgInputTokens  int     `json:"avg_input_tokens"`
			AvgOutputTokens int     `json:"avg_output_tokens"`
			DurationMs      float64 `json:"duration_ms"`
			Config          struct {
				Concurrency int `json:"concurrency"`
				Rounds      int `json:"rounds"`
			} `json:"config"`
		} `json:"result"`
	}

	// resourceUsage tracks GPU/memory usage from the benchmark for overlay YAML.
	resourceUsage := result.ResourceUsage

	if result.TotalCells > 0 {
		// Matrix benchmark — extract representative cell for overlay
		repCell, ok := extractRepresentativeCell(result.ResponseJSON)
		if !ok {
			slog.Info("exploration: no representative cell found in matrix")
			return
		}
		repResult, _ := repCell["result"].(map[string]any)
		if repResult == nil {
			return
		}
		// Use resource_usage from representative cell if available
		if ru, ok := repCell["resource_usage"].(map[string]any); ok && len(ru) > 0 {
			resourceUsage = ru
		}
		if benchmarkID, _ := repCell["benchmark_id"].(string); benchmarkID != "" {
			result.BenchmarkID = benchmarkID
		}
		if configID, _ := repCell["config_id"].(string); configID != "" {
			result.ConfigID = configID
		}
		if engineVersion, _ := repCell["engine_version"].(string); engineVersion != "" {
			result.EngineVersion = engineVersion
		}
		if engineImage, _ := repCell["engine_image"].(string); engineImage != "" {
			result.EngineImage = engineImage
		}
		if cfg, ok := repCell["deploy_config"].(map[string]any); ok && len(cfg) > 0 {
			result.DeployConfig = cloneAnyMap(cfg)
		}
		// Marshal the representative cell's result and re-parse into envelope
		wrapped, _ := json.Marshal(map[string]any{"result": repResult})
		if err := json.Unmarshal(wrapped, &envelope); err != nil {
			slog.Warn("exploration: cannot parse representative cell", "error", err)
			return
		}
	} else {
		// Single-point benchmark (tune tasks) — existing logic
		if err := json.Unmarshal([]byte(result.ResponseJSON), &envelope); err != nil {
			slog.Warn("exploration: cannot parse benchmark for knowledge creation", "error", err)
			return
		}
	}
	bench := envelope.Result

	// Only create knowledge if benchmark actually produced meaningful data
	if bench.ThroughputTPS <= 0 || bench.ErrorRate >= 0.5 {
		slog.Info("exploration: skipping knowledge creation — no meaningful benchmark data",
			"tps", bench.ThroughputTPS, "error_rate", bench.ErrorRate)
		return
	}

	sourceConfig := deployCfg
	if len(result.DeployConfig) > 0 {
		sourceConfig = result.DeployConfig
	}

	// Start with the deploy config passed from ensureDeployed or benchmark output.
	deployConfig := make(map[string]any)
	for k, v := range sourceConfig {
		deployConfig[k] = v
	}

	// Also merge SearchSpace entries (from tune tasks)
	for k, vals := range plan.SearchSpace {
		if len(vals) > 0 {
			deployConfig[k] = vals[0]
		}
	}

	// O13: use GPU arch (e.g. "Ada") for variant matching, not profile name
	gpuArch := plan.Target.GPUArch
	if gpuArch == "" {
		gpuArch = plan.Target.Hardware
	}
	variantName := fmt.Sprintf("%s-%s-%s-explorer",
		plan.Target.Model, gpuArch, plan.Target.Engine)

	// Performance bounds from measured data
	tpsLow := int(bench.ThroughputTPS * 0.8)
	tpsHigh := int(bench.ThroughputTPS * 1.2)
	if tpsLow < 1 {
		tpsLow = 1
	}
	ttftLow := int(bench.TTFTP50ms)
	ttftHigh := int(bench.TTFTP95ms)
	if ttftHigh < ttftLow {
		ttftHigh = ttftLow
	}

	// Build default_config — filter internal keys (starting with '.') and nil values.
	// INV-1: internal args list comes from engine YAML, not hardcoded Go.
	internalSet := make(map[string]bool, len(plan.Target.InternalArgs))
	for _, k := range plan.Target.InternalArgs {
		internalSet[k] = true
	}
	defaultConfig := make(map[string]any)
	for k, v := range deployConfig {
		if strings.HasPrefix(k, ".") || v == nil {
			continue
		}
		if internalSet[k] {
			continue
		}
		defaultConfig[k] = v
	}

	modelType := plan.Target.ModelType
	if modelType == "" {
		modelType = "llm"
	}

	// Infer VRAM from benchmark resource usage
	vramMinMiB := 0
	if resourceUsage != nil {
		if v, ok := resourceUsage["vram_usage_mib"]; ok {
			switch vf := v.(type) {
			case float64:
				vramMinMiB = int(vf)
			case int:
				vramMinMiB = vf
			}
		}
	}

	// Prefer catalog metadata for family/parameter_count (INV-1: knowledge from YAML),
	// fall back to name-based inference for models not yet in the catalog.
	family := plan.Target.Family
	if family == "" {
		family = inferModelFamily(plan.Target.Model)
	}
	paramCount := plan.Target.ParameterCount
	if paramCount == "" {
		paramCount = inferParameterCount(plan.Target.Model)
	}

	// Build structured overlay as a map and marshal to YAML.
	overlay := map[string]any{
		"kind": "model_asset",
		"metadata": map[string]any{
			"name":            plan.Target.Model,
			"type":            modelType,
			"family":          family,
			"parameter_count": paramCount,
			"notes": fmt.Sprintf("Auto-discovered by Explorer on %s. Benchmark ID: %s. Engine version: %s. Engine image: %s.",
				time.Now().Format("2006-01-02"), result.BenchmarkID, result.EngineVersion, result.EngineImage),
		},
		"storage": map[string]any{
			"formats": []string{"safetensors", "gguf"},
		},
		"variants": []map[string]any{{
			"name": variantName,
			"hardware": map[string]any{
				"gpu_arch":     gpuArch,
				"vram_min_mib": vramMinMiB,
			},
			"engine":         plan.Target.Engine,
			"format":         "safetensors",
			"default_config": defaultConfig,
			"expected_performance": map[string]any{
				"tokens_per_second":      []int{tpsLow, tpsHigh},
				"latency_first_token_ms": []int{ttftLow, ttftHigh},
				"throughput_tps":         bench.ThroughputTPS,
				"qps":                    bench.QPS,
				"ttft_p50_ms":            bench.TTFTP50ms,
				"ttft_p95_ms":            bench.TTFTP95ms,
				"ttft_p99_ms":            bench.TTFTP99ms,
				"tpot_p50_ms":            bench.TPOTP50ms,
				"tpot_p95_ms":            bench.TPOTP95ms,
				"concurrency":            bench.Config.Concurrency,
				"avg_input_tokens":       bench.AvgInputTokens,
				"avg_output_tokens":      bench.AvgOutputTokens,
				"error_rate":             bench.ErrorRate,
				"notes": fmt.Sprintf("Explorer auto-discovered %s. Benchmark ID: %s",
					time.Now().Format("2006-01-02T15:04:05Z"), result.BenchmarkID),
			},
		}},
	}

	yamlBytes, err := yaml.Marshal(overlay)
	if err != nil {
		slog.Warn("exploration: failed to marshal knowledge overlay YAML", "error", err)
		return
	}

	// Write via catalog.override MCP tool.
	// D4: only allow auto-promote when benchmark metadata is complete.
	overrideMap := map[string]any{
		"kind":    "model_asset",
		"name":    plan.Target.Model,
		"content": string(yamlBytes),
	}
	if result.TotalCells > 0 {
		// Matrix benchmark — auto-promote if at least half the cells succeeded
		if result.SuccessCells >= result.TotalCells/2 {
			overrideMap["auto_promote"] = true
		}
	} else if benchmarkMetadataComplete(bench.Config.Concurrency, bench.Config.Rounds, bench.TotalRequests) {
		overrideMap["auto_promote"] = true
	} else {
		slog.Info("exploration: overlay created but auto-promote skipped (incomplete benchmark metadata)",
			"concurrency", bench.Config.Concurrency, "rounds", bench.Config.Rounds,
			"requests", bench.TotalRequests)
	}
	overrideArgs, _ := json.Marshal(overrideMap)

	overrideResult, err := m.tools.ExecuteTool(ctx, "catalog.override", overrideArgs)
	if err != nil {
		slog.Warn("exploration: failed to create knowledge overlay",
			"model", plan.Target.Model, "error", err)
		return
	}

	slog.Info("exploration: created knowledge overlay from benchmark",
		"model", plan.Target.Model, "engine", plan.Target.Engine,
		"tps", bench.ThroughputTPS, "result", overrideResult)

	// Record as exploration event
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:       run.ID,
		StepIndex:   99, // post-benchmark knowledge creation step
		StepKind:    "knowledge_create",
		Status:      "completed",
		ToolName:    "catalog.override",
		RequestJSON: string(overrideArgs),
		ResponseJSON: func() string {
			if overrideResult != nil {
				return overrideResult.Content
			}
			return ""
		}(),
		ArtifactType: "model_asset_overlay",
		ArtifactID:   plan.Target.Model,
	})
}

// benchmarkMetadataComplete returns true if benchmark was run with meaningful parameters.
// D4: prevents auto-promotion of configs tested with zero concurrency/rounds.
func benchmarkMetadataComplete(concurrency, rounds, totalRequests int) bool {
	return concurrency > 0 && rounds > 0 && totalRequests > 0
}

// Representative cell scoring: prefer concurrency=1 (heavily penalized via multiplier),
// then prefer cells closest to typical mid-range usage (1024 tokens in/out).
const (
	repCellConcurrencyPenalty = 1000 // score penalty per unit of concurrency
	repCellTargetTokens       = 1024 // target input/output token count
)

// extractRepresentativeCell picks the most representative benchmark cell for overlay YAML.
func extractRepresentativeCell(matrixJSON string) (map[string]any, bool) {
	var resp struct {
		MatrixProfiles []json.RawMessage `json:"matrix_profiles"`
	}
	if err := json.Unmarshal([]byte(matrixJSON), &resp); err != nil {
		return nil, false
	}

	type candidate struct {
		cell  map[string]any
		score int // lower = better
	}
	var best *candidate
	for _, profileJSON := range resp.MatrixProfiles {
		var profile struct {
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
				DeployConfig  map[string]any `json:"deploy_config,omitempty"`
			} `json:"cells"`
		}
		if json.Unmarshal(profileJSON, &profile) != nil {
			continue
		}
		for _, c := range profile.Cells {
			if c.Error != "" || !meaningfulBenchmarkResult(c.Result) {
				continue
			}
			score := c.Concurrency * repCellConcurrencyPenalty
			if d := c.InputTokens - repCellTargetTokens; d < 0 {
				score -= d
			} else {
				score += d
			}
			if d := c.MaxTokens - repCellTargetTokens; d < 0 {
				score -= d
			} else {
				score += d
			}
			cellMap := map[string]any{
				"concurrency":  c.Concurrency,
				"input_tokens": c.InputTokens,
				"max_tokens":   c.MaxTokens,
				"result":       c.Result,
			}
			if c.BenchmarkID != "" {
				cellMap["benchmark_id"] = c.BenchmarkID
			}
			if c.ConfigID != "" {
				cellMap["config_id"] = c.ConfigID
			}
			if c.EngineVersion != "" {
				cellMap["engine_version"] = c.EngineVersion
			}
			if c.EngineImage != "" {
				cellMap["engine_image"] = c.EngineImage
			}
			if len(c.ResourceUsage) > 0 {
				cellMap["resource_usage"] = c.ResourceUsage
			}
			if len(c.DeployConfig) > 0 {
				cellMap["deploy_config"] = c.DeployConfig
			}
			if best == nil || score < best.score {
				best = &candidate{cell: cellMap, score: score}
			}
		}
	}
	if best == nil {
		return nil, false
	}
	return best.cell, true
}

// parseDeployConfig extracts the config map from a deploy.apply JSON response.
func parseDeployConfig(responseJSON string) map[string]any {
	if responseJSON == "" {
		return nil
	}
	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal([]byte(responseJSON), &resp); err != nil || len(resp.Config) == 0 {
		return nil
	}
	slog.Info("exploration: parsed deploy config for overlay YAML", "keys", len(resp.Config))
	return resp.Config
}

// inferModelFamily extracts the model family from a model name by prefix matching.
func inferModelFamily(model string) string {
	lower := strings.ToLower(model)
	for _, f := range []struct{ prefix, family string }{
		{"qwen", "qwen"}, {"glm", "glm"}, {"llama", "llama"},
		{"mistral", "mistral"}, {"deepseek", "deepseek"},
		{"minicpm", "minicpm"}, {"phi", "phi"}, {"gemma", "gemma"},
		{"baichuan", "baichuan"}, {"internlm", "internlm"},
		{"chatglm", "glm"}, {"codegeex", "glm"},
	} {
		if strings.HasPrefix(lower, f.prefix) {
			return f.family
		}
	}
	return "unknown"
}

var paramCountRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)[Bb]`)

// inferParameterCount extracts the parameter count (e.g. "3B") from a model name.
func inferParameterCount(model string) string {
	if m := paramCountRe.FindStringSubmatch(model); len(m) > 1 {
		return m[1] + "B"
	}
	return "unknown"
}

func (m *ExplorationManager) executeOpenQuestion(ctx context.Context, run *state.ExplorationRun) {
	if m.tools == nil {
		run.Status = "failed"
		run.Error = "exploration open_question requires tool executor"
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}
	if run.SourceRef == "" {
		run.Status = "failed"
		run.Error = "exploration open_question requires source_ref"
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	question, err := m.db.GetOpenQuestion(ctx, run.SourceRef)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	var plan ExplorationPlan
	if err := json.Unmarshal([]byte(run.PlanJSON), &plan); err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("parse exploration plan: %v", err)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}
	if plan.Target.Model == "" {
		run.Status = "failed"
		run.Error = "exploration open_question requires target.model for automated validation"
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	run.Status = "running"
	run.StartedAt = time.Now()
	_ = m.db.UpdateExplorationRun(context.Background(), run)

	// Pre-flight: ensure the model is deployed before benchmarking.
	lease, err := m.ensureDeployed(ctx, run, plan)
	if err != nil {
		m.finishRunWithError(run, fmt.Errorf("pre-flight deploy: %w", err))
		return
	}
	if lease != nil {
		defer m.releaseDeployment(lease)
	}
	if m.finishRunIfContextDone(ctx, run) {
		return
	}

	stepResult, err := m.executeBenchmarkStep(ctx, run, plan, "resolve_open_question", 0)
	if err != nil {
		if stepResult != nil {
			run.SummaryJSON = stepResult.ResponseJSON
		}
		m.finishRunWithError(run, err)
		return
	}
	if m.finishRunIfContextDone(ctx, run) {
		return
	}

	actualResult := buildOpenQuestionActualResult(question, plan, stepResult)
	hardware := firstNonEmpty(plan.Target.Hardware, run.HardwareID, question.Hardware)
	if err := m.db.ResolveOpenQuestion(context.Background(), question.ID, "tested", actualResult, hardware); err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("resolve open question %s: %v", question.ID, err)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	resolveReq, _ := json.Marshal(map[string]any{
		"action":   "resolve",
		"id":       question.ID,
		"status":   "tested",
		"result":   actualResult,
		"hardware": hardware,
	})
	resolveResp, _ := json.Marshal(map[string]any{
		"status": "resolved",
		"id":     question.ID,
	})
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:        run.ID,
		StepIndex:    1,
		StepKind:     "resolve_open_question",
		Status:       "completed",
		ToolName:     "knowledge.open_questions",
		RequestJSON:  string(resolveReq),
		ResponseJSON: string(resolveResp),
		ArtifactType: "open_question",
		ArtifactID:   question.ID,
	})

	run.Status = "completed"
	run.CompletedAt = time.Now()
	run.SummaryJSON = actualResult
	_ = m.db.UpdateExplorationRun(context.Background(), run)
}

func (m *ExplorationManager) cleanup(runID, kind, modelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeRuns, runID)
	delete(m.activeProbeInfo, modelID)
	if kind == "tune" && m.tuneRunID == runID {
		m.tuneRunID = ""
	}
}

func (m *ExplorationManager) newRun(ctx context.Context, req ExplorationStart) (*state.ExplorationRun, error) {
	if req.Kind == "" {
		req.Kind = "tune"
	}
	if req.Kind != "tune" && req.Kind != "validate" && req.Kind != "open_question" {
		return nil, fmt.Errorf("exploration kind %q not implemented", req.Kind)
	}
	if req.Kind == "open_question" && req.SourceRef == "" {
		return nil, fmt.Errorf("source_ref is required for open_question exploration")
	}
	if req.Executor == "" {
		req.Executor = "local_go"
	}
	if req.Executor != "local_go" {
		return nil, fmt.Errorf("executor %q not implemented", req.Executor)
	}
	if req.RequestedBy == "" {
		req.RequestedBy = "user"
	}
	if req.ApprovalMode == "" {
		req.ApprovalMode = "none"
	}
	var openQuestion *state.OpenQuestion
	if req.SourceRef != "" && m.db != nil {
		openQuestion, _ = m.db.GetOpenQuestion(ctx, req.SourceRef)
	}
	if req.Goal == "" {
		switch req.Kind {
		case "open_question":
			if openQuestion != nil && openQuestion.Question != "" {
				req.Goal = fmt.Sprintf("validate open question: %s", openQuestion.Question)
			} else {
				req.Goal = fmt.Sprintf("validate open question %s", req.SourceRef)
			}
		case "validate":
			req.Goal = fmt.Sprintf("validate %s", req.Target.Model)
		default:
			req.Goal = fmt.Sprintf("tune %s", req.Target.Model)
		}
	}

	plan := ExplorationPlan{
		Kind:              req.Kind,
		Goal:              req.Goal,
		SourceRef:         req.SourceRef,
		Target:            req.Target,
		EngineParams:      req.EngineParams,
		SearchSpace:       req.SearchSpace,
		Constraints:       req.Constraints,
		BenchmarkProfiles: req.BenchmarkProfiles,
	}
	if plan.Target.Model == "" {
		return nil, fmt.Errorf("target.model is required")
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return nil, fmt.Errorf("marshal exploration plan: %w", err)
	}

	h := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", req.Kind, req.Target.Model, time.Now().UnixNano())))
	return &state.ExplorationRun{
		ID:           hex.EncodeToString(h[:8]),
		Kind:         req.Kind,
		Goal:         plan.Goal,
		RequestedBy:  req.RequestedBy,
		Executor:     req.Executor,
		Planner:      "none",
		Status:       "queued",
		HardwareID:   plan.Target.Hardware,
		EngineID:     plan.Target.Engine,
		ModelID:      plan.Target.Model,
		SourceRef:    req.SourceRef,
		ApprovalMode: req.ApprovalMode,
		PlanJSON:     string(planJSON),
	}, nil
}

func (m *ExplorationManager) executeBenchmarkStep(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan, stepKind string, stepIndex int) (*benchmarkStepResult, error) {
	if isMatrixBenchmark(plan.BenchmarkProfiles) {
		return m.executeBenchmarkMatrix(ctx, run, plan, stepKind, stepIndex)
	}
	bp := firstProfile(plan.BenchmarkProfiles)
	var deployStep *deploymentStepResult
	if strings.TrimSpace(bp.Endpoint) == "" {
		var err error
		deployStep, err = m.executeDeployStep(ctx, run, plan, stepKind, stepIndex)
		if err != nil {
			return nil, err
		}
	}
	benchArgs := map[string]any{
		"model":       plan.Target.Model,
		"concurrency": bp.Concurrency,
		"rounds":      bp.Rounds,
	}
	if modality := benchmarkModality(plan.Target.ModelType); modality != "" {
		benchArgs["modality"] = modality
	}
	if bp.Endpoint != "" {
		benchArgs["endpoint"] = bp.Endpoint
	} else if deployStep != nil && deployStep.Endpoint != "" {
		benchArgs["endpoint"] = deployStep.Endpoint
	}
	if plan.Target.Hardware != "" {
		benchArgs["hardware"] = plan.Target.Hardware
		benchArgs["save"] = true
	}
	if plan.Target.Engine != "" {
		benchArgs["engine"] = plan.Target.Engine
	}
	if deployStep != nil && len(deployStep.Config) > 0 {
		benchArgs["deploy_config"] = deployStep.Config
	}
	if _, ok := benchArgs["save"]; !ok {
		benchArgs["save"] = false
	}
	if bp.Concurrency <= 0 {
		benchArgs["concurrency"] = 1
	}
	if bp.Rounds <= 0 {
		benchArgs["rounds"] = 1
	}

	requestJSON, _ := json.Marshal(benchArgs)
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:       run.ID,
		StepIndex:   stepIndex,
		StepKind:    stepKind,
		Status:      "running",
		ToolName:    "benchmark.run",
		RequestJSON: string(requestJSON),
	})

	result, err := m.tools.ExecuteTool(ctx, "benchmark.run", requestJSON)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		responseJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
			RunID:        run.ID,
			StepIndex:    stepIndex,
			StepKind:     stepKind,
			Status:       "failed",
			ToolName:     "benchmark.run",
			RequestJSON:  string(requestJSON),
			ResponseJSON: string(responseJSON),
		})
		return nil, err
	}

	var summary struct {
		BenchmarkID   string         `json:"benchmark_id"`
		ConfigID      string         `json:"config_id"`
		EngineVersion string         `json:"engine_version"`
		EngineImage   string         `json:"engine_image"`
		ResourceUsage map[string]any `json:"resource_usage"`
		DeployConfig  map[string]any `json:"deploy_config"`
	}
	_ = json.Unmarshal([]byte(result.Content), &summary)

	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:        run.ID,
		StepIndex:    stepIndex,
		StepKind:     stepKind,
		Status:       "completed",
		ToolName:     "benchmark.run",
		RequestJSON:  string(requestJSON),
		ResponseJSON: result.Content,
		ArtifactType: "benchmark_result",
		ArtifactID:   summary.BenchmarkID,
	})

	return &benchmarkStepResult{
		RequestJSON:   string(requestJSON),
		ResponseJSON:  result.Content,
		BenchmarkID:   summary.BenchmarkID,
		ConfigID:      summary.ConfigID,
		EngineVersion: summary.EngineVersion,
		EngineImage:   summary.EngineImage,
		ResourceUsage: cloneAnyMap(summary.ResourceUsage),
		DeployConfig:  cloneAnyMap(summary.DeployConfig),
	}, nil
}

func (m *ExplorationManager) executeBenchmarkMatrix(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan, stepKind string, stepIndex int) (*benchmarkStepResult, error) {
	endpoint := firstProfile(plan.BenchmarkProfiles).Endpoint // resolved by executeValidate
	deployConfig := m.resolveCurrentDeployConfig(ctx, plan.Target.Model, plan.Target.Engine)

	var allCellsJSON []json.RawMessage
	totalCells, successCells := 0, 0

	for i, profile := range plan.BenchmarkProfiles {
		matrixArgs := map[string]any{
			"model":              plan.Target.Model,
			"endpoint":           endpoint,
			"concurrency_levels": profile.ConcurrencyLevels,
			"input_token_levels": profile.InputTokenLevels,
			"max_token_levels":   profile.MaxTokenLevels,
			"requests_per_combo": profile.RequestsPerCombo,
			"rounds":             profile.Rounds,
			"save":               true,
		}
		if modality := benchmarkModality(plan.Target.ModelType); modality != "" {
			matrixArgs["modality"] = modality
		}
		if len(deployConfig) > 0 {
			matrixArgs["deploy_config"] = deployConfig
		}
		if plan.Target.Hardware != "" {
			matrixArgs["hardware"] = plan.Target.Hardware
		}
		if plan.Target.Engine != "" {
			matrixArgs["engine"] = plan.Target.Engine
		}

		requestJSON, _ := json.Marshal(matrixArgs)
		profileLabel := profile.Label
		if profileLabel == "" {
			profileLabel = "unknown"
		}
		_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
			RunID:       run.ID,
			StepIndex:   stepIndex + i,
			StepKind:    stepKind,
			Status:      "running",
			ToolName:    "benchmark.matrix",
			RequestJSON: string(requestJSON),
		})

		slog.Info("explorer: running benchmark matrix", "profile", profileLabel,
			"concurrency", profile.ConcurrencyLevels,
			"input_tokens", profile.InputTokenLevels,
			"output_tokens", profile.MaxTokenLevels)

		result, err := m.tools.ExecuteTool(ctx, "benchmark.matrix", requestJSON)
		if err == nil {
			err = toolResultError(result)
		}

		if err != nil {
			slog.Warn("explorer: benchmark matrix failed", "profile", profileLabel, "error", err)
			_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
				RunID:     run.ID,
				StepIndex: stepIndex + i,
				StepKind:  stepKind,
				Status:    "failed",
				ToolName:  "benchmark.matrix",
			})
			continue
		}

		var matrixResp struct {
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
				DeployConfig  map[string]any `json:"deploy_config,omitempty"`
			} `json:"cells"`
			Total int `json:"total"`
		}
		_ = json.Unmarshal([]byte(result.Content), &matrixResp)

		for _, cell := range matrixResp.Cells {
			totalCells++
			if cell.Error == "" && meaningfulBenchmarkResult(cell.Result) {
				successCells++
			}
		}

		// Wrap profile response with label for downstream note generation
		wrapped, _ := json.Marshal(map[string]any{
			"label": profileLabel,
			"cells": matrixResp.Cells,
		})
		allCellsJSON = append(allCellsJSON, json.RawMessage(wrapped))

		_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
			RunID:        run.ID,
			StepIndex:    stepIndex + i,
			StepKind:     stepKind,
			Status:       "completed",
			ToolName:     "benchmark.matrix",
			ResponseJSON: result.Content,
		})
	}

	payload := map[string]any{
		"matrix_profiles": allCellsJSON,
		"total_cells":     totalCells,
		"success_cells":   successCells,
		"deploy_config":   cloneAnyMap(deployConfig),
	}
	combinedJSON, _ := json.Marshal(payload)
	if repCell, ok := extractRepresentativeCell(string(combinedJSON)); ok {
		if repResult, ok := repCell["result"]; ok {
			payload["result"] = repResult
		}
		if benchmarkID, _ := repCell["benchmark_id"].(string); benchmarkID != "" {
			payload["benchmark_id"] = benchmarkID
		}
		if configID, _ := repCell["config_id"].(string); configID != "" {
			payload["config_id"] = configID
		}
		if engineVersion, _ := repCell["engine_version"].(string); engineVersion != "" {
			payload["engine_version"] = engineVersion
		}
		if engineImage, _ := repCell["engine_image"].(string); engineImage != "" {
			payload["engine_image"] = engineImage
		}
		if usage, ok := repCell["resource_usage"].(map[string]any); ok && len(usage) > 0 {
			payload["resource_usage"] = usage
		}
		if cfg, ok := repCell["deploy_config"].(map[string]any); ok && len(cfg) > 0 {
			payload["deploy_config"] = cloneAnyMap(cfg)
		}
	}
	combinedJSON, _ = json.Marshal(payload)

	stepResult := &benchmarkStepResult{
		ResponseJSON:  string(combinedJSON),
		MatrixJSON:    string(combinedJSON),
		BenchmarkID:   firstNonEmptyJSON(payload, "benchmark_id"),
		ConfigID:      firstNonEmptyJSON(payload, "config_id"),
		EngineVersion: firstNonEmptyJSON(payload, "engine_version"),
		EngineImage:   firstNonEmptyJSON(payload, "engine_image"),
		ResourceUsage: cloneAnyMap(mapValue(payload, "resource_usage")),
		DeployConfig:  cloneAnyMap(mapValue(payload, "deploy_config")),
		TotalCells:    totalCells,
		SuccessCells:  successCells,
	}
	if totalCells == 0 {
		return nil, fmt.Errorf("benchmark matrix: no cells returned")
	}
	if successCells == 0 {
		return stepResult, fmt.Errorf("benchmark matrix: no successful cells (total=%d)", totalCells)
	}
	return stepResult, nil
}

func mapValue(payload map[string]any, key string) map[string]any {
	if payload == nil {
		return nil
	}
	value, _ := payload[key].(map[string]any)
	return value
}

func (m *ExplorationManager) executeDeployStep(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan, stepKind string, stepIndex int) (*deploymentStepResult, error) {
	deployArgs := map[string]any{
		"model":   plan.Target.Model,
		"no_pull": true,
	}
	if plan.Target.Engine != "" {
		deployArgs["engine"] = plan.Target.Engine
	}

	requestJSON, _ := json.Marshal(deployArgs)
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:       run.ID,
		StepIndex:   stepIndex,
		StepKind:    stepKind,
		Status:      "running",
		ToolName:    "deploy.run",
		RequestJSON: string(requestJSON),
	})

	result, err := m.tools.ExecuteTool(ctx, "deploy.run", requestJSON)
	if err == nil {
		err = toolResultError(result)
	}
	if err != nil {
		responseJSON, _ := json.Marshal(map[string]string{"error": err.Error()})
		_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
			RunID:        run.ID,
			StepIndex:    stepIndex,
			StepKind:     stepKind,
			Status:       "failed",
			ToolName:     "deploy.run",
			RequestJSON:  string(requestJSON),
			ResponseJSON: string(responseJSON),
		})
		return nil, err
	}

	var summary struct {
		Name    string         `json:"name"`
		Address string         `json:"address"`
		Config  map[string]any `json:"config"`
	}
	_ = json.Unmarshal([]byte(result.Content), &summary)
	endpoint := openAIChatCompletionsEndpoint(summary.Address)
	if endpoint == "" {
		return nil, fmt.Errorf("deploy.run did not return a ready address")
	}

	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:        run.ID,
		StepIndex:    stepIndex,
		StepKind:     stepKind,
		Status:       "completed",
		ToolName:     "deploy.run",
		RequestJSON:  string(requestJSON),
		ResponseJSON: result.Content,
		ArtifactType: "deployment",
		ArtifactID:   summary.Name,
	})

	return &deploymentStepResult{
		RequestJSON:  string(requestJSON),
		ResponseJSON: result.Content,
		Address:      summary.Address,
		Endpoint:     endpoint,
		Config:       summary.Config,
	}, nil
}

func buildOpenQuestionActualResult(question *state.OpenQuestion, plan ExplorationPlan, stepResult *benchmarkStepResult) string {
	payload := map[string]any{
		"question_id":    question.ID,
		"question":       question.Question,
		"hypothesis":     question.Expected,
		"test_method":    question.TestCommand,
		"target":         plan.Target,
		"benchmark_id":   stepResult.BenchmarkID,
		"config_id":      stepResult.ConfigID,
		"engine_version": stepResult.EngineVersion,
		"engine_image":   stepResult.EngineImage,
	}
	if len(stepResult.ResourceUsage) > 0 {
		payload["resource_usage"] = stepResult.ResourceUsage
	}
	if len(stepResult.DeployConfig) > 0 {
		payload["deploy_config"] = stepResult.DeployConfig
	}
	var benchmark any
	if err := json.Unmarshal([]byte(stepResult.ResponseJSON), &benchmark); err == nil {
		payload["benchmark"] = benchmark
	} else {
		payload["benchmark_raw"] = stepResult.ResponseJSON
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func openAIChatCompletionsEndpoint(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if !strings.HasPrefix(address, "http://") && !strings.HasPrefix(address, "https://") {
		address = "http://" + address
	}
	return strings.TrimRight(address, "/") + "/v1/chat/completions"
}

func buildTuningParams(searchSpace map[string][]any) []TunableParam {
	if len(searchSpace) == 0 {
		return nil
	}
	keys := make([]string, 0, len(searchSpace))
	for key := range searchSpace {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	params := make([]TunableParam, 0, len(searchSpace))
	for _, key := range keys {
		params = append(params, TunableParam{
			Key:    key,
			Values: searchSpace[key],
		})
	}
	return params
}
