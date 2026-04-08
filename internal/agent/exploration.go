package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	state "github.com/jguan/aima/internal"
	"gopkg.in/yaml.v3"
)

const gpuReleaseGrace = 3 * time.Second // grace period for GPU memory to fully release from driver

type ExplorationTarget struct {
	Hardware string `json:"hardware,omitempty"`
	GPUArch  string `json:"gpu_arch,omitempty"` // e.g. "Ada" — for overlay YAML, not resolution
	Model    string `json:"model"`
	Engine   string `json:"engine,omitempty"`
}

type ExplorationConstraints struct {
	MaxCandidates int `json:"max_candidates,omitempty"`
}

type ExplorationBenchmarkProfile struct {
	Endpoint    string `json:"endpoint,omitempty"`
	Concurrency int    `json:"concurrency,omitempty"`
	Rounds      int    `json:"rounds,omitempty"`
}

type ExplorationPlan struct {
	Kind             string                      `json:"kind"`
	Goal             string                      `json:"goal"`
	Target           ExplorationTarget           `json:"target"`
	SourceRef        string                      `json:"source_ref,omitempty"`
	SearchSpace      map[string][]any            `json:"search_space,omitempty"`
	Constraints      ExplorationConstraints      `json:"constraints,omitempty"`
	BenchmarkProfile ExplorationBenchmarkProfile `json:"benchmark_profile,omitempty"`
}

type benchmarkStepResult struct {
	RequestJSON  string
	ResponseJSON string
	BenchmarkID  string
	ConfigID     string
}

type deploymentStepResult struct {
	RequestJSON  string
	ResponseJSON string
	Address      string
	Endpoint     string
	Config       map[string]any
}

type ExplorationStart struct {
	Kind         string                      `json:"kind"`
	Goal         string                      `json:"goal"`
	Target       ExplorationTarget           `json:"target"`
	Executor     string                      `json:"executor,omitempty"`
	RequestedBy  string                      `json:"requested_by,omitempty"`
	ApprovalMode string                      `json:"approval_mode,omitempty"`
	SourceRef    string                      `json:"source_ref,omitempty"`
	SearchSpace  map[string][]any            `json:"search_space,omitempty"`
	Constraints  ExplorationConstraints      `json:"constraints,omitempty"`
	Benchmark    ExplorationBenchmarkProfile `json:"benchmark_profile,omitempty"`
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

	mu         sync.Mutex
	activeRuns map[string]context.CancelFunc
	tuneRunID  string
}

func NewExplorationManager(db *state.DB, tuner *Tuner, tools ToolExecutor) *ExplorationManager {
	return &ExplorationManager{
		db:         db,
		tuner:      tuner,
		tools:      tools,
		activeRuns: make(map[string]context.CancelFunc),
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
	timeout := time.After(30 * time.Minute)
	for {
		select {
		case <-ctx.Done():
			return m.Stop(context.Background(), run.ID)
		case <-timeout:
			_, _ = m.Stop(context.Background(), run.ID)
			return nil, fmt.Errorf("exploration %s timed out after 30 minutes", run.ID)
		case <-ticker.C:
			status, err := m.Status(ctx, run.ID)
			if err != nil {
				return nil, err
			}
			switch status.Run.Status {
			case "completed", "failed", "cancelled":
				return status, nil
			}
		}
	}
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

func (m *ExplorationManager) execute(ctx context.Context, run *state.ExplorationRun) {
	defer m.cleanup(run.ID, run.Kind)

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

	tuningConfig := TuningConfig{
		Model:       plan.Target.Model,
		Hardware:    plan.Target.Hardware,
		Engine:      plan.Target.Engine,
		Endpoint:    plan.BenchmarkProfile.Endpoint,
		Parameters:  buildTuningParams(plan.SearchSpace),
		Concurrency: plan.BenchmarkProfile.Concurrency,
		Rounds:      plan.BenchmarkProfile.Rounds,
		MaxConfigs:  plan.Constraints.MaxCandidates,
	}

	requestJSON, _ := json.Marshal(map[string]any{
		"action":      "start",
		"model":       tuningConfig.Model,
		"hardware":    tuningConfig.Hardware,
		"engine":      tuningConfig.Engine,
		"endpoint":    tuningConfig.Endpoint,
		"parameters":  tuningConfig.Parameters,
		"concurrency": tuningConfig.Concurrency,
		"rounds":      tuningConfig.Rounds,
		"max_configs": tuningConfig.MaxConfigs,
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

		summaryJSON, _ := json.Marshal(current)
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
	deployCfg, err := m.ensureDeployed(ctx, run, plan)
	if err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("pre-flight deploy: %s", err)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	// Resolve actual endpoint from deploy.status — the model may be on a non-default port.
	if plan.BenchmarkProfile.Endpoint == "" {
		if addr := m.resolveDeployEndpoint(ctx, plan.Target.Model); addr != "" {
			plan.BenchmarkProfile.Endpoint = addr
			slog.Info("exploration: resolved benchmark endpoint from deployment",
				"model", plan.Target.Model, "endpoint", addr)
		}
	}

	stepResult, err := m.executeBenchmarkStep(ctx, run, plan, "validate", 0)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	run.Status = "completed"
	run.CompletedAt = time.Now()
	run.SummaryJSON = stepResult.ResponseJSON
	_ = m.db.UpdateExplorationRun(context.Background(), run)

	// Task #13: After successful benchmark, write discovered knowledge as overlay YAML.
	m.maybeCreateKnowledge(ctx, run, plan, stepResult, deployCfg)
}

// ensureDeployed deploys the target model+engine with config if not already running.
// Returns the resolved deployment config (for overlay YAML creation).
// B17: checks deploy.status first — if already ready, skip deploy; if starting, wait;
// only call deploy.apply when no existing deployment or previous one failed.
func (m *ExplorationManager) ensureDeployed(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan) (map[string]any, error) {
	if m.tools == nil {
		return nil, fmt.Errorf("no tool executor")
	}

	// B17: Pre-check — avoid "already starting" errors by inspecting current state.
	phase, ready := m.checkDeployStatus(ctx, plan.Target.Model)
	if ready {
		slog.Info("exploration: model already deployed and ready, skipping deploy",
			"model", plan.Target.Model, "engine", plan.Target.Engine)
		return nil, nil
	}
	if phase == "starting" || phase == "pulling" {
		slog.Info("exploration: model already deploying, waiting for ready",
			"model", plan.Target.Model, "phase", phase)
		if err := m.waitForReady(ctx, plan.Target.Model); err != nil {
			return nil, fmt.Errorf("wait for in-progress deploy %s: %w", plan.Target.Model, err)
		}
		return nil, nil
	}

	// B25: stop any OTHER running native deployment to free the single native slot.
	m.stopConflictingDeploy(ctx, plan.Target.Model)

	args := map[string]any{
		"model":     plan.Target.Model,
		"engine":    plan.Target.Engine,
		"auto_pull": false, // Explorer must never download — only use locally available resources.
	}
	// Flatten SearchSpace into config overrides for deploy.
	if len(plan.SearchSpace) > 0 {
		config := make(map[string]any, len(plan.SearchSpace))
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

	slog.Info("exploration: model deployed for validation",
		"model", plan.Target.Model, "engine", plan.Target.Engine)

	// B14: Wait for the service to become ready before benchmarking.
	if err := m.waitForReady(ctx, plan.Target.Model); err != nil {
		slog.Warn("exploration: readiness check failed, proceeding anyway",
			"model", plan.Target.Model, "error", err)
	}
	return deployCfg, nil
}

// checkDeployStatus returns the current phase and readiness of a deployment.
// Returns ("", false) if the deployment doesn't exist or status can't be determined.
func (m *ExplorationManager) checkDeployStatus(ctx context.Context, model string) (string, bool) {
	statusArgs, _ := json.Marshal(map[string]string{"name": model})
	result, err := m.tools.ExecuteTool(ctx, "deploy.status", statusArgs)
	if err != nil || result == nil {
		return "", false
	}
	var status struct {
		Phase string `json:"phase"`
		Ready bool   `json:"ready"`
	}
	if json.Unmarshal([]byte(result.Content), &status) != nil {
		return "", false
	}
	return status.Phase, status.Ready
}

// stopConflictingDeploy stops any running deployment that is NOT the target model.
// B25: native runtime only supports one deployment at a time — port conflicts and
// GPU memory exhaustion prevent concurrent native deployments.
func (m *ExplorationManager) stopConflictingDeploy(ctx context.Context, targetModel string) {
	listResult, err := m.tools.ExecuteTool(ctx, "deploy.list", []byte("{}"))
	if err != nil || listResult == nil {
		return
	}
	var deploys []struct {
		Name    string `json:"name"`
		Phase   string `json:"phase"`
		Runtime string `json:"runtime"`
	}
	if json.Unmarshal([]byte(listResult.Content), &deploys) != nil {
		return
	}
	for _, d := range deploys {
		if d.Name == targetModel {
			continue
		}
		// Only delete deployments that are actively holding GPU resources.
		// Failed/stopped deployments already released GPU — skip entirely.
		if d.Phase != "running" && d.Phase != "starting" {
			continue
		}
		slog.Info("exploration: deleting conflicting deployment to free slot",
			"stopping", d.Name, "for", targetModel, "runtime", d.Runtime, "phase", d.Phase)
		deleteArgs, _ := json.Marshal(map[string]string{"name": d.Name})
		if _, err := m.tools.ExecuteTool(ctx, "deploy.delete", deleteArgs); err != nil {
			slog.Warn("exploration: failed to delete conflicting deployment", "name", d.Name, "error", err)
		}
		m.waitForDeleteComplete(ctx, d.Name)
	}
}

// waitForDeleteComplete polls deploy.status until the deployment is no longer running.
func (m *ExplorationManager) waitForDeleteComplete(ctx context.Context, name string) {
	waitForGPURelease(ctx, m.tools, name, gpuReleaseGrace)
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

// waitForReady polls deploy.status until the deployment reports ready, or timeout.
// B18: detects terminal failure phases (failed/stopped/error) and returns immediately
// instead of wasting the full timeout on a crashed process.
func (m *ExplorationManager) waitForReady(ctx context.Context, model string) error {
	if m.tools == nil {
		return nil
	}
	const (
		pollInterval = 5 * time.Second
		maxWait      = 5 * time.Minute
	)
	deadline := time.Now().Add(maxWait)
	for time.Now().Before(deadline) {
		statusArgs, _ := json.Marshal(map[string]string{"name": model})
		result, err := m.tools.ExecuteTool(ctx, "deploy.status", statusArgs)
		if err == nil && result != nil {
			var status struct {
				Phase string `json:"phase"`
				Ready bool   `json:"ready"`
			}
			if json.Unmarshal([]byte(result.Content), &status) == nil {
				if status.Ready {
					slog.Info("exploration: service ready", "model", model)
					return nil
				}
				// B18: fail fast on terminal phases — process crashed or was stopped.
				switch status.Phase {
				case "failed", "stopped", "error", "exited":
					return fmt.Errorf("deployment %s entered terminal phase %q", model, status.Phase)
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	return fmt.Errorf("timeout waiting for %s to become ready after %v", model, maxWait)
}

// resolveDeployEndpoint queries deploy.status to get the actual inference address.
// Returns an OpenAI-compatible endpoint URL or empty string.
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
	return fmt.Sprintf("http://%s/v1/chat/completions", status.Address)
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
	if err := json.Unmarshal([]byte(result.ResponseJSON), &envelope); err != nil {
		slog.Warn("exploration: cannot parse benchmark for knowledge creation", "error", err)
		return
	}
	bench := envelope.Result

	// Only create knowledge if benchmark actually produced meaningful data
	if bench.ThroughputTPS <= 0 || bench.ErrorRate >= 0.5 {
		slog.Info("exploration: skipping knowledge creation — no meaningful benchmark data",
			"tps", bench.ThroughputTPS, "error_rate", bench.ErrorRate)
		return
	}

	// Start with the deploy config passed from ensureDeployed.
	deployConfig := make(map[string]any)
	for k, v := range deployCfg {
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
	defaultConfig := make(map[string]any)
	for k, v := range deployConfig {
		if strings.HasPrefix(k, ".") || v == nil {
			continue
		}
		defaultConfig[k] = v
	}

	// Build structured overlay as a map and marshal to YAML.
	overlay := map[string]any{
		"kind": "model_asset",
		"metadata": map[string]any{
			"name":            plan.Target.Model,
			"type":            "llm",
			"family":          "explorer-discovered",
			"parameter_count": "unknown",
			"notes":           fmt.Sprintf("Auto-discovered by Explorer on %s", time.Now().Format("2006-01-02")),
		},
		"storage": map[string]any{
			"formats": []string{"safetensors", "gguf"},
		},
		"variants": []map[string]any{{
			"name": variantName,
			"hardware": map[string]any{
				"gpu_arch":     gpuArch,
				"vram_min_mib": 0,
			},
			"engine":         plan.Target.Engine,
			"format":         "safetensors",
			"default_config": defaultConfig,
			"expected_performance": map[string]any{
				"tokens_per_second":       []int{tpsLow, tpsHigh},
				"latency_first_token_ms":  []int{ttftLow, ttftHigh},
				"throughput_tps":          bench.ThroughputTPS,
				"qps":                     bench.QPS,
				"ttft_p50_ms":             bench.TTFTP50ms,
				"ttft_p95_ms":             bench.TTFTP95ms,
				"ttft_p99_ms":             bench.TTFTP99ms,
				"tpot_p50_ms":             bench.TPOTP50ms,
				"tpot_p95_ms":             bench.TPOTP95ms,
				"concurrency":             bench.Config.Concurrency,
				"avg_input_tokens":        bench.AvgInputTokens,
				"avg_output_tokens":       bench.AvgOutputTokens,
				"error_rate":              bench.ErrorRate,
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

	// Write via catalog.override MCP tool
	overrideArgs, _ := json.Marshal(map[string]string{
		"kind":    "model_asset",
		"name":    plan.Target.Model,
		"content": string(yamlBytes),
	})

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
		RunID:        run.ID,
		StepIndex:    99, // post-benchmark knowledge creation step
		StepKind:     "knowledge_create",
		Status:       "completed",
		ToolName:     "catalog.override",
		RequestJSON:  string(overrideArgs),
		ResponseJSON: func() string { if overrideResult != nil { return overrideResult.Content }; return "" }(),
		ArtifactType: "model_asset_overlay",
		ArtifactID:   plan.Target.Model,
	})
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
	if _, err := m.ensureDeployed(ctx, run, plan); err != nil {
		run.Status = "failed"
		run.Error = fmt.Sprintf("pre-flight deploy: %s", err)
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
		return
	}

	stepResult, err := m.executeBenchmarkStep(ctx, run, plan, "resolve_open_question", 0)
	if err != nil {
		run.Status = "failed"
		run.Error = err.Error()
		run.CompletedAt = time.Now()
		_ = m.db.UpdateExplorationRun(context.Background(), run)
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

func (m *ExplorationManager) cleanup(runID, kind string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.activeRuns, runID)
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
		Kind:             req.Kind,
		Goal:             req.Goal,
		SourceRef:        req.SourceRef,
		Target:           req.Target,
		SearchSpace:      req.SearchSpace,
		Constraints:      req.Constraints,
		BenchmarkProfile: req.Benchmark,
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
	var deployStep *deploymentStepResult
	if strings.TrimSpace(plan.BenchmarkProfile.Endpoint) == "" {
		var err error
		deployStep, err = m.executeDeployStep(ctx, run, plan, stepKind, stepIndex)
		if err != nil {
			return nil, err
		}
	}
	benchArgs := map[string]any{
		"model":       plan.Target.Model,
		"concurrency": plan.BenchmarkProfile.Concurrency,
		"rounds":      plan.BenchmarkProfile.Rounds,
	}
	if plan.BenchmarkProfile.Endpoint != "" {
		benchArgs["endpoint"] = plan.BenchmarkProfile.Endpoint
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
	if plan.BenchmarkProfile.Concurrency <= 0 {
		benchArgs["concurrency"] = 1
	}
	if plan.BenchmarkProfile.Rounds <= 0 {
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
		BenchmarkID string `json:"benchmark_id"`
		ConfigID    string `json:"config_id"`
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
		RequestJSON:  string(requestJSON),
		ResponseJSON: result.Content,
		BenchmarkID:  summary.BenchmarkID,
		ConfigID:     summary.ConfigID,
	}, nil
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
		"question_id":  question.ID,
		"question":     question.Question,
		"hypothesis":   question.Expected,
		"test_method":  question.TestCommand,
		"target":       plan.Target,
		"benchmark_id": stepResult.BenchmarkID,
		"config_id":    stepResult.ConfigID,
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
