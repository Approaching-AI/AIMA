package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	state "github.com/jguan/aima/internal"
)

type ExplorationTarget struct {
	Hardware string `json:"hardware,omitempty"`
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
