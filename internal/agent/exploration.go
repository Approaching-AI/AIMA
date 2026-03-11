package agent

import (
	"bytes"
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

type explorationZeroClaw interface {
	Available() bool
	Plan(ctx context.Context, request json.RawMessage) (json.RawMessage, error)
}

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

type explorationPlannerRequest struct {
	Kind             string                      `json:"kind"`
	Goal             string                      `json:"goal"`
	SourceRef        string                      `json:"source_ref,omitempty"`
	Target           ExplorationTarget           `json:"target"`
	SearchSpace      map[string][]any            `json:"search_space,omitempty"`
	Constraints      ExplorationConstraints      `json:"constraints,omitempty"`
	BenchmarkProfile ExplorationBenchmarkProfile `json:"benchmark_profile,omitempty"`
	OpenQuestion     *plannerOpenQuestion        `json:"open_question,omitempty"`
}

type plannerOpenQuestion struct {
	ID          string `json:"id"`
	SourceAsset string `json:"source_asset,omitempty"`
	Question    string `json:"question"`
	TestCommand string `json:"test_command,omitempty"`
	Expected    string `json:"expected,omitempty"`
	Status      string `json:"status,omitempty"`
}

type benchmarkStepResult struct {
	RequestJSON  string
	ResponseJSON string
	BenchmarkID  string
	ConfigID     string
}

type ExplorationStart struct {
	Kind         string                      `json:"kind"`
	Goal         string                      `json:"goal"`
	Target       ExplorationTarget           `json:"target"`
	Planner      string                      `json:"planner,omitempty"`
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
	db       *state.DB
	tuner    *Tuner
	tools    ToolExecutor
	zeroclaw explorationZeroClaw

	mu         sync.Mutex
	activeRuns map[string]context.CancelFunc
	tuneRunID  string
}

func NewExplorationManager(db *state.DB, tuner *Tuner, tools ToolExecutor, zeroclaw explorationZeroClaw) *ExplorationManager {
	return &ExplorationManager{
		db:         db,
		tuner:      tuner,
		tools:      tools,
		zeroclaw:   zeroclaw,
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

	requestJSON, _ := json.Marshal(tuningConfig)
	_ = m.db.InsertExplorationEvent(context.Background(), &state.ExplorationEvent{
		RunID:       run.ID,
		StepIndex:   0,
		StepKind:    "tune",
		Status:      "running",
		ToolName:    "tuning.start",
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
			ToolName:     "tuning.start",
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
				ToolName:     "tuning.start",
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
				ToolName:     "tuning.start",
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
				ToolName:     "tuning.start",
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
	if req.Planner == "" {
		req.Planner = "none"
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
	if req.Planner == "zeroclaw" {
		if m.zeroclaw == nil || !m.zeroclaw.Available() {
			return nil, fmt.Errorf("zeroclaw planner requested but unavailable")
		}
		planned, err := m.planWithZeroClaw(ctx, req, openQuestion)
		if err != nil {
			return nil, err
		}
		plan = planned
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
		Planner:      req.Planner,
		Status:       "queued",
		HardwareID:   plan.Target.Hardware,
		EngineID:     plan.Target.Engine,
		ModelID:      plan.Target.Model,
		SourceRef:    req.SourceRef,
		ApprovalMode: req.ApprovalMode,
		PlanJSON:     string(planJSON),
	}, nil
}

func (m *ExplorationManager) planWithZeroClaw(ctx context.Context, req ExplorationStart, openQuestion *state.OpenQuestion) (ExplorationPlan, error) {
	var plannerQuestion *plannerOpenQuestion
	if openQuestion != nil {
		plannerQuestion = &plannerOpenQuestion{
			ID:          openQuestion.ID,
			SourceAsset: openQuestion.SourceAsset,
			Question:    openQuestion.Question,
			TestCommand: openQuestion.TestCommand,
			Expected:    openQuestion.Expected,
			Status:      openQuestion.Status,
		}
	}
	request := explorationPlannerRequest{
		Kind:             req.Kind,
		Goal:             req.Goal,
		SourceRef:        req.SourceRef,
		Target:           req.Target,
		SearchSpace:      req.SearchSpace,
		Constraints:      req.Constraints,
		BenchmarkProfile: req.Benchmark,
		OpenQuestion:     plannerQuestion,
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return ExplorationPlan{}, fmt.Errorf("marshal planner request: %w", err)
	}
	response, err := m.zeroclaw.Plan(ctx, payload)
	if err != nil {
		return ExplorationPlan{}, fmt.Errorf("plan with zeroclaw: %w", err)
	}
	plan, err := decodeExplorationPlan(response)
	if err != nil {
		return ExplorationPlan{}, fmt.Errorf("decode zeroclaw plan: %w", err)
	}
	plan = mergePlannedExploration(req, plan)
	if plan.Kind == "" {
		plan.Kind = req.Kind
	}
	if plan.Kind != req.Kind {
		return ExplorationPlan{}, fmt.Errorf("zeroclaw planner returned kind %q for requested kind %q", plan.Kind, req.Kind)
	}
	return plan, nil
}

func mergePlannedExploration(req ExplorationStart, plan ExplorationPlan) ExplorationPlan {
	if plan.Kind == "" {
		plan.Kind = req.Kind
	}
	if plan.Goal == "" {
		plan.Goal = req.Goal
	}
	if plan.SourceRef == "" {
		plan.SourceRef = req.SourceRef
	}
	if plan.Target.Model == "" {
		plan.Target.Model = req.Target.Model
	}
	if plan.Target.Engine == "" {
		plan.Target.Engine = req.Target.Engine
	}
	if plan.Target.Hardware == "" {
		plan.Target.Hardware = req.Target.Hardware
	}
	if len(plan.SearchSpace) == 0 {
		plan.SearchSpace = req.SearchSpace
	}
	if plan.Constraints.MaxCandidates == 0 {
		plan.Constraints = req.Constraints
	}
	if plan.BenchmarkProfile.Endpoint == "" {
		plan.BenchmarkProfile.Endpoint = req.Benchmark.Endpoint
	}
	if plan.BenchmarkProfile.Concurrency == 0 {
		plan.BenchmarkProfile.Concurrency = req.Benchmark.Concurrency
	}
	if plan.BenchmarkProfile.Rounds == 0 {
		plan.BenchmarkProfile.Rounds = req.Benchmark.Rounds
	}
	return plan
}

func decodeExplorationPlan(raw json.RawMessage) (ExplorationPlan, error) {
	candidates := []json.RawMessage{bytes.TrimSpace(raw)}
	if extracted := extractJSONObject(string(raw)); extracted != "" && extracted != string(raw) {
		candidates = append(candidates, json.RawMessage(extracted))
	}

	for _, candidate := range candidates {
		if len(candidate) == 0 {
			continue
		}
		var wrapper struct {
			Plan json.RawMessage `json:"plan"`
		}
		if err := json.Unmarshal(candidate, &wrapper); err == nil && len(wrapper.Plan) > 0 {
			var plan ExplorationPlan
			if err := json.Unmarshal(wrapper.Plan, &plan); err == nil {
				return plan, nil
			}
		}

		var plan ExplorationPlan
		if err := json.Unmarshal(candidate, &plan); err == nil {
			return plan, nil
		}
	}

	return ExplorationPlan{}, fmt.Errorf("no valid exploration plan JSON in zeroclaw response")
}

func extractJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		trimmed = strings.TrimPrefix(trimmed, "```json")
		trimmed = strings.TrimPrefix(trimmed, "```JSON")
		trimmed = strings.TrimPrefix(trimmed, "```")
		if idx := strings.LastIndex(trimmed, "```"); idx >= 0 {
			trimmed = trimmed[:idx]
		}
	}
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return ""
	}
	return strings.TrimSpace(trimmed[start : end+1])
}

func (m *ExplorationManager) executeBenchmarkStep(ctx context.Context, run *state.ExplorationRun, plan ExplorationPlan, stepKind string, stepIndex int) (*benchmarkStepResult, error) {
	benchArgs := map[string]any{
		"model":       plan.Target.Model,
		"concurrency": plan.BenchmarkProfile.Concurrency,
		"rounds":      plan.BenchmarkProfile.Rounds,
	}
	if plan.BenchmarkProfile.Endpoint != "" {
		benchArgs["endpoint"] = plan.BenchmarkProfile.Endpoint
	}
	if plan.Target.Hardware != "" {
		benchArgs["hardware"] = plan.Target.Hardware
		benchArgs["save"] = true
	}
	if plan.Target.Engine != "" {
		benchArgs["engine"] = plan.Target.Engine
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
