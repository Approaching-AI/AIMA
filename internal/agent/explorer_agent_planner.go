package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// ExplorerAgentPlanner implements Planner using a tool-calling agent loop.
type ExplorerAgentPlanner struct {
	llm       LLMClient
	workspace *ExplorerWorkspace
	tools     *ExplorerToolExecutor
	queryFn   QueryFunc
	phaseFn   func(string)
	maxCycles int
	maxTasks  int
	lastInput PlanInput
}

// ExplorerAgentPlannerOption configures the ExplorerAgentPlanner.
type ExplorerAgentPlannerOption func(*ExplorerAgentPlanner)

// WithAgentMaxCycles sets the max PDCA iterations per round.
func WithAgentMaxCycles(n int) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.maxCycles = n }
}

// WithAgentMaxTasks sets the max tasks per plan.
func WithAgentMaxTasks(n int) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.maxTasks = n }
}

// WithAgentQueryFunc sets the knowledge base query function.
func WithAgentQueryFunc(fn QueryFunc) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.queryFn = fn }
}

// WithAgentPhaseObserver mirrors planner sub-phases (plan/check/act) to the
// outer Explorer so operator-facing status stays aligned with live execution.
func WithAgentPhaseObserver(fn func(string)) ExplorerAgentPlannerOption {
	return func(p *ExplorerAgentPlanner) { p.phaseFn = fn }
}

// NewExplorerAgentPlanner creates a new agent planner.
func NewExplorerAgentPlanner(llm LLMClient, workspace *ExplorerWorkspace, opts ...ExplorerAgentPlannerOption) *ExplorerAgentPlanner {
	p := &ExplorerAgentPlanner{
		llm:       llm,
		workspace: workspace,
		maxCycles: 3,
		maxTasks:  5,
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// runPhase executes one agent loop phase (plan, check, or act).
// The LLM reads/writes workspace documents via tool calls until it calls done().
// Returns total tokens used.
func (p *ExplorerAgentPlanner) runPhase(ctx context.Context, phase, systemPrompt string) (int, error) {
	tools := NewExplorerToolExecutor(p.workspace, p.queryFn)
	toolDefs := tools.ToolDefinitions()
	if p.phaseFn != nil {
		p.phaseFn(phase)
	}

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: phaseUserPrompt(phase)},
	}

	var totalTokens int
	maxTurns := phaseMaxTurns(phase)

	for turn := 0; turn < maxTurns; turn++ {
		select {
		case <-ctx.Done():
			return totalTokens, ctx.Err()
		default:
		}

		var resp *Response
		var err error
		slog.Info("explorer agent: phase request", "phase", phase, "turn", turn)
		if streamer, ok := p.llm.(StreamingLLMClient); ok {
			resp, err = streamer.ChatCompletionStream(ctx, messages, toolDefs, func(delta CompletionDelta) {
				if !llmOutputLoggingEnabled() {
					return
				}
				slog.Info("explorer agent: llm delta",
					"phase", phase,
					"turn", turn,
					"content", delta.Content,
					"reasoning_content", delta.ReasoningContent,
					"tool_calls", delta.ToolCalls)
			})
		} else {
			resp, err = p.llm.ChatCompletion(ctx, messages, toolDefs)
		}
		if err != nil {
			return totalTokens, fmt.Errorf("LLM call in %s phase (turn %d): %w", phase, turn, err)
		}
		if llmOutputLoggingEnabled() {
			slog.Info("explorer agent: llm response",
				"phase", phase,
				"turn", turn,
				"content", resp.Content,
				"reasoning_content", resp.ReasoningContent,
				"tool_calls", resp.ToolCalls,
				"prompt_tokens", resp.PromptTokens,
				"completion_tokens", resp.CompletionTokens,
				"total_tokens", resp.TotalTokens)
		}
		totalTokens += resp.TotalTokens

		if len(resp.ToolCalls) == 0 {
			if err := p.captureAssistantOnlyOutput(phase, tools, resp); err != nil {
				return totalTokens, err
			}
			slog.Info("explorer agent: phase ended (no tool calls)",
				"phase", phase,
				"turn", turn,
				"content_present", strings.TrimSpace(resp.Content) != "")
			break
		}

		messages = append(messages, Message{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ToolCalls:        resp.ToolCalls,
		})

		for _, tc := range resp.ToolCalls {
			slog.Debug("explorer agent: tool call", "phase", phase, "tool", tc.Name)
			result := tools.Execute(tc.Name, json.RawMessage(tc.Arguments))

			content := result.Content
			if result.IsError {
				content = "error: " + content
			}
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})

			if tools.Done() {
				slog.Info("explorer agent: phase done", "phase", phase, "verdict", tools.Verdict(), "turn", turn)
				p.tools = tools
				return totalTokens, nil
			}
		}
	}

	p.tools = tools
	return totalTokens, nil
}

func phaseUserPrompt(phase string) string {
	switch phase {
	case "plan":
		return "开始 plan 阶段。先读 index.md、available-combos.md、knowledge-base.md（特别是 Pending Work 和 Frontier Coverage）、experiment-facts.md，以及 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger；只能从 Ready Combos 选任务，且必须避开当前循环的阻塞和拒绝重试项；优先消化 Pending Work，再考虑新的模型/引擎覆盖；tune 任务要把固定参数写成 search_space 里的单元素数组；不要发明标准镜像、隐藏变体或不在 Ready Combos 里的任务；完成后写入完整的 plan.md 并调用 done()。"
	case "check":
		return "开始 check 阶段。先读 experiment-facts.md，再读取实验结果与 summary.md；逐个回填 experiments/*.md 的 Agent Notes（每个 3-5 行分析），再更新 summary.md（含横向对比表和场景标注的 Recommended Configurations）；Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger 写成结构化内容；如果 summary.md 与 experiment-facts.md 冲突，以 experiment-facts.md 为准；用 done(verdict) 结束。"
	case "act":
		return "开始 act 阶段。先读 experiment-facts.md，再读 knowledge-base.md 的 Pending Work，并基于 summary.md 修订 plan.md；只能追加新的 Ready Combos 任务，且不得命中 Do Not Retry This Cycle；如果上一轮事实已经表明某 combo blocked 或不在 Ready Combos，就不要重提；tune 任务要把固定参数写成 search_space 里的单元素数组；调用 done()。"
	default:
		return "继续当前阶段。必须使用工具读写工作区，并在完成时调用 done。"
	}
}

func taskBlockedByExecutableFacts(model, engine string, comboFacts []ComboFact, skipCombos []SkipCombo) (string, bool) {
	key := planTaskComboKey(model, engine)
	if key == "" {
		return "missing model or engine", true
	}

	blockedFacts := make(map[string]string, len(comboFacts)+len(skipCombos))
	readyFacts := make(map[string]struct{}, len(comboFacts))
	allowOnlyReady := len(comboFacts) > 0
	for _, fact := range comboFacts {
		factKey := planTaskComboKey(fact.Model, fact.Engine)
		if factKey == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(fact.Status)) {
		case "ready":
			readyFacts[factKey] = struct{}{}
		case "blocked":
			reason := strings.TrimSpace(fact.Reason)
			if reason == "" {
				reason = "blocked by combo facts"
			}
			blockedFacts[factKey] = reason
		}
	}
	for _, skip := range skipCombos {
		if strings.EqualFold(strings.TrimSpace(skip.Model), strings.TrimSpace(model)) && strings.TrimSpace(skip.Engine) == "" {
			reason := strings.TrimSpace(skip.Reason)
			if reason == "" {
				reason = "blocked at model scope"
			}
			return reason, true
		}
		skipKey := planTaskComboKey(skip.Model, skip.Engine)
		if skipKey == "" {
			continue
		}
		if _, exists := blockedFacts[skipKey]; exists {
			continue
		}
		reason := strings.TrimSpace(skip.Reason)
		if reason == "" {
			reason = "already explored in this round"
		}
		blockedFacts[skipKey] = reason
	}
	if reason, blocked := blockedFacts[key]; blocked {
		return reason, true
	}
	if allowOnlyReady {
		if _, ok := readyFacts[key]; !ok {
			return "not in ready combos", true
		}
	}
	return "", false
}

func (p *ExplorerAgentPlanner) filterTaskSpecs(input PlanInput, tasks []TaskSpec) []TaskSpec {
	allowedParams := allowedEngineParams(input.LocalEngines)

	filtered := make([]TaskSpec, 0, len(tasks))
	for _, task := range tasks {
		if reason, blocked := taskBlockedByExecutableFacts(task.Model, task.Engine, input.ComboFacts, input.SkipCombos); blocked {
			slog.Info("explorer agent: task denied by executable facts", "model", task.Model, "engine", task.Engine, "reason", reason)
			continue
		}
		task.EngineParams = sanitizeTaskEngineParams(task.Engine, task.EngineParams, allowedParams)
		task.SearchSpace = sanitizeTaskSearchSpace(task.Engine, task.SearchSpace, allowedParams)
		if strings.EqualFold(strings.TrimSpace(task.Kind), "tune") {
			task.SearchSpace = mergeTuneTaskSearchSpace(task.EngineParams, task.SearchSpace)
			if !hasRealTuneSearchSpace(task.SearchSpace) {
				slog.Info("explorer agent: dropped tune task without a real search space", "model", task.Model, "engine", task.Engine)
				continue
			}
		}
		filtered = append(filtered, task)
	}
	return rebalanceTaskSpecs(input, filtered, p.maxTasks)
}

func allowedEngineParams(engines []LocalEngine) map[string]map[string]struct{} {
	allowed := make(map[string]map[string]struct{}, len(engines)*2)
	for _, engine := range engines {
		params := make(map[string]struct{}, len(engine.TunableParams))
		for key := range engine.TunableParams {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			params[key] = struct{}{}
		}
		for _, alias := range []string{engine.Name, engine.Type} {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			allowed[alias] = params
		}
	}
	return allowed
}

func sanitizeTaskEngineParams(engine string, params map[string]any, allowedByEngine map[string]map[string]struct{}) map[string]any {
	if len(params) == 0 {
		return nil
	}
	allowed := allowedByEngine[strings.TrimSpace(engine)]
	if len(allowed) == 0 {
		if len(params) > 0 {
			slog.Info("explorer agent: stripped non-tunable engine params", "engine", engine, "count", len(params))
		}
		return nil
	}
	sanitized := make(map[string]any, len(params))
	for key, value := range params {
		if _, ok := allowed[key]; !ok {
			slog.Info("explorer agent: stripped unknown engine param", "engine", engine, "param", key)
			continue
		}
		sanitized[key] = value
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func sanitizeTaskSearchSpace(engine string, searchSpace map[string][]any, allowedByEngine map[string]map[string]struct{}) map[string][]any {
	if len(searchSpace) == 0 {
		return nil
	}
	allowed := allowedByEngine[strings.TrimSpace(engine)]
	if len(allowed) == 0 {
		if len(searchSpace) > 0 {
			slog.Info("explorer agent: stripped non-tunable search space", "engine", engine, "count", len(searchSpace))
		}
		return nil
	}
	sanitized := make(map[string][]any, len(searchSpace))
	for key, values := range searchSpace {
		if _, ok := allowed[key]; !ok {
			slog.Info("explorer agent: stripped unknown search-space param", "engine", engine, "param", key)
			continue
		}
		if len(values) == 0 {
			continue
		}
		cp := make([]any, len(values))
		copy(cp, values)
		sanitized[key] = cp
	}
	if len(sanitized) == 0 {
		return nil
	}
	return sanitized
}

func mergeTuneTaskSearchSpace(engineParams map[string]any, searchSpace map[string][]any) map[string][]any {
	merged := cloneSearchSpace(searchSpace)
	if len(engineParams) == 0 {
		return merged
	}
	if merged == nil {
		merged = make(map[string][]any, len(engineParams))
	}
	for key, value := range engineParams {
		if _, exists := merged[key]; exists {
			continue
		}
		merged[key] = []any{value}
	}
	return merged
}

func hasRealTuneSearchSpace(searchSpace map[string][]any) bool {
	for _, values := range searchSpace {
		if len(values) <= 1 {
			continue
		}
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			seen[fmt.Sprintf("%#v", value)] = struct{}{}
		}
		if len(seen) > 1 {
			return true
		}
	}
	return false
}

func hasSummaryBenchmarkSignal(perf PerfSummary) bool {
	return perf.ThroughputTPS > 0 || perf.LatencyP50Ms > 0
}

func phaseMaxTurns(phase string) int {
	switch strings.ToLower(strings.TrimSpace(phase)) {
	case "act":
		return 8
	case "check":
		return 12
	default:
		return 12
	}
}

func hasMatchingExperimentEvidence(result ExperimentResult) bool {
	completed := strings.EqualFold(strings.TrimSpace(result.Status), "completed")
	if completed && (result.SuccessCells > 0 || result.BenchmarkID != "" || result.ConfigID != "") {
		return true
	}
	for _, bench := range result.Benchmarks {
		if bench.ThroughputTPS > 0 {
			return true
		}
		if completed && (bench.BenchmarkID != "" || bench.ConfigID != "") {
			return true
		}
	}
	return false
}

func parseBenchmarkScenario(spec string) (concurrency, inputTokens, maxTokens int, ok bool) {
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, value, found := strings.Cut(part, "=")
		if !found {
			return 0, 0, 0, false
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, 0, 0, false
		}
		switch strings.TrimSpace(key) {
		case "concurrency":
			concurrency = n
		case "input":
			inputTokens = n
		case "max_tokens":
			maxTokens = n
		}
	}
	return concurrency, inputTokens, maxTokens, concurrency > 0 && inputTokens > 0 && maxTokens > 0
}

func benchmarkLatencyP50Ms(bench BenchmarkEntry) float64 {
	if bench.LatencyP50Ms > 0 {
		return bench.LatencyP50Ms
	}
	if bench.TTFTP50Ms <= 0 && bench.TPOTP50Ms <= 0 {
		return 0
	}
	maxTokens := bench.MaxTokens
	if maxTokens < 0 {
		maxTokens = 0
	}
	return bench.TTFTP50Ms + (float64(maxTokens) * bench.TPOTP50Ms)
}

func hasMatchingLatencyEvidence(perf PerfSummary, records []ExperimentRecord, model, engine string) bool {
	if perf.LatencyP50Ms <= 0 {
		return true
	}
	concurrency, inputTokens, maxTokens, ok := parseBenchmarkScenario(perf.LatencyScenario)
	if !ok {
		return false
	}
	targetKey := strings.ToLower(strings.TrimSpace(model)) + "|" + strings.ToLower(strings.TrimSpace(engine))
	for _, rec := range records {
		recKey := strings.ToLower(strings.TrimSpace(rec.Task.Model)) + "|" + strings.ToLower(strings.TrimSpace(rec.Task.Engine))
		if recKey != targetKey || !hasMatchingExperimentEvidence(rec.Result) {
			continue
		}
		for _, bench := range rec.Result.Benchmarks {
			if bench.Concurrency != concurrency || bench.InputTokens != inputTokens || bench.MaxTokens != maxTokens {
				continue
			}
			latency := benchmarkLatencyP50Ms(bench)
			if latency <= 0 {
				return false
			}
			delta := perf.LatencyP50Ms - latency
			if delta < 0 {
				delta = -delta
			}
			tolerance := latency * 0.15
			if tolerance < 50 {
				tolerance = 50
			}
			return delta <= tolerance
		}
	}
	return false
}

func (p *ExplorerAgentPlanner) validateRecommendationConfidence() error {
	if p.workspace == nil {
		return nil
	}
	configs, err := p.workspace.ExtractRecommendations()
	if err != nil {
		return err
	}
	records, err := p.workspace.LoadExperimentRecords()
	if err != nil {
		return err
	}
	evidence := make(map[string]bool, len(records))
	for _, rec := range records {
		key := strings.ToLower(strings.TrimSpace(rec.Task.Model)) + "|" + strings.ToLower(strings.TrimSpace(rec.Task.Engine))
		if hasMatchingExperimentEvidence(rec.Result) {
			evidence[key] = true
		}
	}
	// Collect downgrades to apply in a single pass. An unknown confidence value
	// is the only hard error — everything else the guard can self-heal by
	// forcing the confidence back to `provisional`, which keeps the PDCA loop
	// running and ensures we never write unbacked `validated`/`tuned` data to
	// the L2c overlay or central.
	downgrades := make(map[string]string, len(configs))
	var firstErr error
	for _, cfg := range configs {
		key := strings.ToLower(strings.TrimSpace(cfg.Model)) + "|" + strings.ToLower(strings.TrimSpace(cfg.Engine))
		level := strings.ToLower(strings.TrimSpace(cfg.Confidence))
		switch level {
		case "", "provisional":
			continue
		case "tuned", "validated":
			var reason string
			switch {
			case !hasSummaryBenchmarkSignal(cfg.Performance):
				reason = fmt.Sprintf("%s/%s marked %s without benchmark evidence", cfg.Model, cfg.Engine, level)
			case len(records) > 0 && !evidence[key]:
				reason = fmt.Sprintf("%s/%s marked %s without matching successful experiment", cfg.Model, cfg.Engine, level)
			case len(records) > 0 && !hasMatchingLatencyEvidence(cfg.Performance, records, cfg.Model, cfg.Engine):
				reason = fmt.Sprintf("%s/%s latency is not grounded by a matching benchmark scenario", cfg.Model, cfg.Engine)
			}
			if reason != "" {
				downgrades[key] = reason
			}
		default:
			if firstErr == nil {
				firstErr = fmt.Errorf("summary recommendation %s/%s has unknown confidence %q", cfg.Model, cfg.Engine, cfg.Confidence)
			}
		}
	}
	if len(downgrades) > 0 {
		applied, err := p.workspace.ForceDowngradeRecommendations(downgrades)
		if err != nil {
			return fmt.Errorf("force-downgrade recommendations: %w", err)
		}
		for _, k := range applied {
			slog.Warn("validation_guard: downgraded recommendation to provisional",
				"key", k, "reason", downgrades[k])
		}
		// Surface a single aggregated error so the caller can still emit the
		// Validation Guard Feedback message into summary.md for the next cycle.
		if firstErr == nil {
			firstErr = fmt.Errorf("downgraded %d recommendation(s) to provisional (evidence missing)", len(applied))
		}
	}
	return firstErr
}

func rebalanceTaskSpecs(input PlanInput, tasks []TaskSpec, maxTasks int) []TaskSpec {
	if len(tasks) <= 1 {
		return tasks
	}
	pendingSet := make(map[string]struct{}, len(input.PendingWork))
	for _, work := range input.PendingWork {
		key := planTaskComboKey(work.Model, work.Engine)
		if key == "" {
			continue
		}
		pendingSet[key] = struct{}{}
	}
	recentModels := make(map[string]bool, len(input.History))
	for _, h := range input.History {
		model := strings.TrimSpace(h.ModelID)
		if model != "" {
			recentModels[model] = true
		}
	}
	familyByModel := make(map[string]string, len(input.LocalModels))
	for _, model := range input.LocalModels {
		name := strings.TrimSpace(model.Name)
		if name == "" {
			continue
		}
		family := strings.TrimSpace(model.Family)
		if family == "" {
			family = inferModelFamily(name)
		}
		familyByModel[name] = family
	}

	selected := make([]TaskSpec, 0, len(tasks))
	used := make([]bool, len(tasks))
	modelCounts := make(map[string]int, len(tasks))
	familyCounts := make(map[string]int, len(tasks))
	appendTask := func(idx int) bool {
		if idx < 0 || idx >= len(tasks) || used[idx] {
			return false
		}
		used[idx] = true
		selected = append(selected, tasks[idx])
		model := strings.TrimSpace(tasks[idx].Model)
		if model != "" {
			modelCounts[model]++
		}
		if family := taskSpecFamily(tasks[idx], familyByModel); family != "" {
			familyCounts[family]++
		}
		return maxTasks > 0 && len(selected) >= maxTasks
	}

	for i, task := range tasks {
		key := planTaskComboKey(task.Model, task.Engine)
		if _, pending := pendingSet[key]; !pending {
			continue
		}
		if appendTask(i) {
			return selected
		}
	}
	for i, task := range tasks {
		if recentModels[strings.TrimSpace(task.Model)] {
			continue
		}
		if appendTask(i) {
			return selected
		}
		break
	}
	for i, task := range tasks {
		if used[i] {
			continue
		}
		if model := strings.TrimSpace(task.Model); model != "" && modelCounts[model] > 0 {
			continue
		}
		if family := taskSpecFamily(task, familyByModel); family != "" && familyCounts[family] > 0 {
			continue
		}
		if appendTask(i) {
			return selected
		}
	}
	for i, task := range tasks {
		if used[i] {
			continue
		}
		if model := strings.TrimSpace(task.Model); model != "" && modelCounts[model] >= 2 {
			continue
		}
		if family := taskSpecFamily(task, familyByModel); family != "" && familyCounts[family] >= 2 {
			continue
		}
		if appendTask(i) {
			return selected
		}
	}
	for i := range tasks {
		if appendTask(i) {
			return selected
		}
	}
	return selected
}

func taskSpecFamily(task TaskSpec, familyByModel map[string]string) string {
	model := strings.TrimSpace(task.Model)
	if model == "" {
		return ""
	}
	if family := strings.TrimSpace(familyByModel[model]); family != "" {
		return family
	}
	return inferModelFamily(model)
}

func (p *ExplorerAgentPlanner) captureAssistantOnlyOutput(phase string, tools *ExplorerToolExecutor, resp *Response) error {
	content := strings.TrimSpace(resp.Content)

	switch phase {
	case "check":
		if content != "" {
			if err := p.workspace.WriteFile("summary.md", normalizeSummaryMarkdown(content)); err != nil {
				return fmt.Errorf("persist assistant summary output: %w", err)
			}
		}
		if tools != nil && tools.Verdict() == "" {
			// Without an explicit done(verdict) tool call, the safest fallback
			// is to stop the PDCA loop instead of inventing extra work.
			tools.verdict = "done"
		}
		return nil
	case "plan", "act":
		if content == "" {
			return fmt.Errorf("assistant returned no tool calls or content in %s phase", phase)
		}
		if err := p.workspace.WriteFile("plan.md", content); err != nil {
			return fmt.Errorf("persist assistant plan output: %w", err)
		}
		return nil
	default:
		return nil
	}
}

// Plan implements Planner: refreshes fact docs, runs Plan phase, parses tasks.
func (p *ExplorerAgentPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	if err := p.workspace.Init(); err != nil {
		return nil, 0, fmt.Errorf("init workspace: %w", err)
	}

	if err := p.RefreshFacts(input); err != nil {
		return nil, 0, fmt.Errorf("refresh fact docs: %w", err)
	}
	if err := p.workspace.EnsureWorkingDocuments(); err != nil {
		return nil, 0, fmt.Errorf("prepare working docs: %w", err)
	}

	prompt := strings.ReplaceAll(planPhaseSystemPrompt, "{max_tasks}", fmt.Sprintf("%d", p.maxTasks))

	tokens, err := p.runPhase(ctx, "plan", prompt)
	if err != nil {
		return nil, tokens, fmt.Errorf("plan phase: %w", err)
	}

	tasks, err := p.workspace.ParsePlan()
	if err != nil {
		return nil, tokens, fmt.Errorf("parse plan: %w", err)
	}

	tasks = p.filterTaskSpecs(input, tasks)

	planTasks := make([]PlanTask, len(tasks))
	for i, ts := range tasks {
		planTasks[i] = taskSpecToPlanTask(ts, input.Hardware.Profile)
	}

	if len(planTasks) > p.maxTasks {
		planTasks = planTasks[:p.maxTasks]
	}

	if len(tasks) == 0 {
		planTasks = nil
	}

	if len(tasks) == 0 && len(planTasks) == 0 {
		return &ExplorerPlan{
			ID:        generatePlanID(),
			Tier:      2,
			Tasks:     nil,
			Reasoning: "agent-planned",
		}, tokens, nil
	}

	plan := &ExplorerPlan{
		ID:        generatePlanID(),
		Tier:      2,
		Tasks:     planTasks,
		Reasoning: "agent-planned",
	}
	return plan, tokens, nil
}

// RefreshFacts updates workspace fact documents from the latest executable
// state and stores the input for subsequent Analyze filtering.
func (p *ExplorerAgentPlanner) RefreshFacts(input PlanInput) error {
	p.lastInput = input
	if p.workspace == nil {
		return nil
	}
	return p.workspace.RefreshFactDocuments(input)
}

// Analyze runs Check phase (+ optional Act phase if verdict="continue").
func (p *ExplorerAgentPlanner) Analyze(ctx context.Context) (string, []TaskSpec, int, error) {
	tokens, err := p.runPhase(ctx, "check", checkPhaseSystemPrompt)
	if err != nil {
		return "", nil, tokens, fmt.Errorf("check phase: %w", err)
	}
	if err := p.validateRecommendationConfidence(); err != nil {
		// Downgrade to warning and inject feedback into workspace for the next
		// Act phase to see. Breaking the PDCA loop here would prevent the LLM
		// from self-correcting in subsequent cycles.
		slog.Warn("explorer agent: validation guard feedback (non-fatal)", "error", err)
		if p.workspace != nil {
			feedback := fmt.Sprintf("\n\n## Validation Guard Feedback\n\n⚠️ %s\n\nDo NOT use `validated` or `tuned` confidence unless summary.md shows benchmark-backed performance and experiment-facts.md contains a matching successful experiment. Downgrade to `provisional` when evidence is missing or only partial.\n", err.Error())
			_ = p.workspace.AppendFile("summary.md", feedback)
		}
	}

	verdict := ""
	if p.tools != nil {
		verdict = p.tools.Verdict()
	}

	if verdict != "continue" {
		return verdict, nil, tokens, nil
	}

	actPrompt := strings.ReplaceAll(actPhaseSystemPrompt, "{max_tasks}", fmt.Sprintf("%d", p.maxTasks))
	actTokens, err := p.runPhase(ctx, "act", actPrompt)
	tokens += actTokens
	if err != nil {
		return verdict, nil, tokens, fmt.Errorf("act phase: %w", err)
	}

	extraTasks, err := p.workspace.ParsePlan()
	if err != nil {
		slog.Warn("explorer agent: parse revised plan failed", "error", err)
		return verdict, nil, tokens, nil
	}
	extraTasks = p.filterTaskSpecs(p.lastInput, extraTasks)

	return verdict, extraTasks, tokens, nil
}

func taskSpecToPlanTask(ts TaskSpec, defaultHardware string) PlanTask {
	params := make(map[string]any)
	for k, v := range ts.EngineParams {
		params[k] = v
	}
	return PlanTask{
		Kind:        ts.Kind,
		Hardware:    defaultHardware,
		Model:       ts.Model,
		Engine:      ts.Engine,
		Params:      params,
		SearchSpace: cloneSearchSpace(ts.SearchSpace),
		Benchmark:   ts.Benchmark,
		Reason:      ts.Reason,
	}
}

func generatePlanID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", timeNow().UnixNano())))
	return fmt.Sprintf("%x", h)[:8]
}

var timeNow = time.Now

const planPhaseSystemPrompt = `你是 AIMA Explorer 的规划代理。你在一个文件工作区中工作，工作区本身就是你的持久上下文。

先执行这个顺序：
1. 先读 index.md
2. 再读 available-combos.md
3. 读 knowledge-base.md（特别是 Pending Work 和 Frontier Coverage）
4. 读 experiment-facts.md
5. 必要时再读 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger（仅作辅助记忆；如果与 available-combos.md 或 experiment-facts.md 冲突，以 executable facts 为准）
6. 必要时再读 device-profile.md、experiments/

强约束：
- index.md 里的规则是最高优先级
- 只有 available-combos.md 中 ## Ready Combos 的组合，才允许出现在新任务里
- 如果组合在 ## Blocked Combos，或者根本不在 ## Ready Combos，就视为不可执行
- summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle 只能作为辅助记忆；若与 available-combos.md / experiment-facts.md 冲突，以 executable facts 为准；如果要重试被阻塞的 family，reason 必须说明 state changed
- engine_params 只能使用 device-profile.md 里列出的 Tunable Params；不要设置 host port、container name、runtime、image 等宿主资源字段
- DC-2: search_space 的语义是“要搜索的取值集合”。每个 key 的 value 必须是 JSON 数组；单元素数组 = 固定值，不会被搜索；多元素数组 = 实际要 sweep 的维度。不要用 engine_params 的 scalar 形式来伪装 tuning。
- DC-3: 如果 benchmark block 指定了 concurrency / input_tokens / max_tokens，这些数组都必须非空。空数组会被当作“用默认值”，但执行时会导致任务实际上没有目标负载点。如果你不打算指定负载，就省略整个 benchmark block；不要写成 []。
- tune 任务如果要做真实参数搜索，必须填写 search_space；需要固定参数时，把它写成 search_space 里的单元素数组，不要把单值 engine_params 伪装成 tuning
- 不要根据常识脑补默认引擎、标准镜像、隐藏模型变体
- query 工具只支持 search、compare、gaps、aggregate
- 你必须通过工具写 plan.md 并调用 done()；不要只输出自然语言

计划目标：
- 每轮最多 {max_tasks} 个任务
- 优先选择最有信息增益、且真实可执行的 Ready Combos
- 先做 validate，只有存在 Pending Work=tune、已有 baseline，或明确理由时才做 tune
- 保持任务多样性，但不要浪费在重复失败组合上
- 如果 knowledge-base.md 的 Pending Work 已经给出同 combo 的 baseline / long-context / tune obligation，优先消费这些 obligation
- 如果存在“未出现在 Recent History 的 Ready 模型”，至少先给其中一个模型分配任务，再考虑继续围绕最近已探索模型做跨引擎 pivot
- 当存在其他未探索 Ready 模型或 family 时，同一 model family 本轮最多保留 2 个任务
- reason 必须说明这个实验为什么值得做，并且它为什么没有被 blocker / denylist 拦住

plan.md 必须保留这些 section：
- ## Objective
- ## Fact Snapshot
- ## Task Board
- ## Tasks

Task Board 应该是人类可读的 checklist，说明这一轮要验证什么。

## Tasks 必须是一个 yaml code block，格式如下：
- kind: validate|tune
  model: <model name>
  engine: <engine type>
  engine_params:
    <key>: <value>
  search_space:
    <key>: [<value>, ...]
  benchmark:
    concurrency: [<int>, ...]
    input_tokens: [<int>, ...]
    max_tokens: [<int>, ...]
    requests_per_combo: <int>
  reason: "<string>"`

const checkPhaseSystemPrompt = `你是 AIMA Explorer 的分析代理。刚执行完一轮实验，你要把结果沉淀成可继续工作的文件记忆。

先执行这个顺序：
1. 读 index.md
2. 读 plan.md
3. 读 experiment-facts.md
4. 读本轮 experiments/*.md
5. 读 device-profile.md 的 ## Active Deployments (Live Snapshot)：这是阶段开始时实时抓取的部署状态；如果本轮实验期间有这些 deploy 仍在占用 GPU/VRAM/端口，必须把它们当作失败的潜在上游原因来考虑（跨任务 handoff 效应），而不是直接归因为 "transient/OOM/startup flakiness"
6. 读 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger（仅作辅助记忆；如果与 experiment-facts.md 冲突，以 experiment-facts.md 为准）
7. 读 summary.md 和 knowledge-base.md（特别是 Pending Work）
8. 逐个回填本轮 experiments/*.md 的 ## Agent Notes（用 append_to_file）
9. 更新 summary.md（含横向对比表）并调用 done(verdict)

回填 Agent Notes 的要求（第 6 步）：
- 用 append_to_file 把分析追加到对应实验文件的 ## Agent Notes section
- 先清除占位符文本 “_To be filled by agent after analysis._”（用 write_to_file 替换整个 Agent Notes section）
- 每个 note 3-5 行，包含：该模型的关键性能特征、与同类模型的对比发现、失败的根因分类
- 失败实验也必须写 notes：根因分析 + 是否为结构性 blocker + 下次可行的规避方案

强约束：
- index.md 里的 summary.md 结构必须保留
- 发现不足时，要明确记录 bugs、失败模式、设计疑虑，而不是只写”失败了”
- 确认 blocker 时必须把它写入 Confirmed Blockers，并把本循环不该再试的项写入 Do Not Retry This Cycle
- Evidence Ledger 只能写事实来源明确的记录，必须区分 this_cycle 和 historical；如果与 experiment-facts.md 冲突，以 experiment-facts.md 为准
- 只有在下一轮仍有高价值 Pending Work 或未消费的高价值 Ready Combos 时，才返回 verdict=”continue”
- **环境性失败判定**：如果本轮所有失败都属于环境/基础设施问题（Docker pull 失败、网络超时、端口冲突、镜像不存在、容器启动崩溃、OOM kill、驱动不兼容等），且没有新的可行 Ready Combos 能绕过这些环境问题，必须返回 verdict=”done”。重试不会修复环境问题，继续只会浪费 token。
- **绝对禁止**：当 throughput_tps 为 0、null 或缺失时，confidence 不允许写 validated 或 tuned，必须写 provisional。这是硬约束，系统会自动拦截违规。
- **延迟字段必须有锚点**：只有当实验矩阵里存在匹配的 benchmark scenario 时，才能填写 latency_p50_ms 和 latency_scenario；不要凭感觉估算。如果只能从 TTFT/TPOT 推出粗略 e2e 代理，就在 note 里写明是 proxy，并让 latency_scenario 精确对应那一格 benchmark。
- **非文本模态**：不要把 image/asr/tts 强行解释成 token TPS；如果事实文件给出的成功信号是 modality-specific throughput，就把它作为 primary throughput 写入 throughput_tps 字段，并在 note 里明确单位。
- **失败模式诊断**：分析 benchmark 矩阵中 status=no-output 的 cell 时，注意区分 input_tokens 单独超限 vs input_tokens+max_tokens 之和超过 max_model_len。对比同模型不同 max_tokens 的 cell 来定位真实边界。
- 你必须通过工具更新 summary.md，并调用 done(verdict)

summary.md 必须保留这些 section：
- ## Key Findings（必须包含横向对比表，见下方格式）
- ## Bugs And Failures
- ## Confirmed Blockers
- ## Do Not Retry This Cycle
- ## Evidence Ledger
- ## Design Doubts
- ## Recommended Configurations
- ## Current Strategy
- ## Next Cycle Candidates

Key Findings 横向对比表格式（按 TPS/GiB 效率降序）：
| 模型 | 大小(GiB) | 峰值TPS | TPS/GiB | 单/双GPU | TPOT P95(ms) | 最佳场景 |

Recommended Configurations 的 YAML 格式：
- model: <name>
  engine: <engine>
  hardware: <profile>
  engine_params: { ... }
  performance:
    throughput_tps: <float>
    throughput_scenario: “<concurrency=N, input=N, max_tokens=N>”
    latency_p50_ms: <float, optional when no measured/proxy latency is available>
    latency_scenario: “<concurrency=1, input=N, max_tokens=N>”
  confidence: validated|tuned|provisional
  note: “<string>”

Confirmed Blockers 的 YAML 格式：
- family: <reason family>
  scope: <combo or broader scope>
  model: <model name>
  engine: <engine>
  reason: <string>
  retry_when: <string>
  confidence: confirmed

Do Not Retry This Cycle 的 YAML 格式：
- model: <model name>
  engine: <engine>
  reason_family: <reason family>
  reason: <string>

Evidence Ledger 的 YAML 格式：
- source: this_cycle|historical
  kind: benchmark|deploy|failure|note
  model: <model name>
  engine: <engine>
  evidence: <string>
  summary: <string>
  confidence: <string>”`

const actPhaseSystemPrompt = `你是 AIMA Explorer 的行动规划代理。你要根据 summary.md 为下一轮 Do 阶段修订计划。

先执行这个顺序：
1. 读 index.md
2. 读 experiment-facts.md
3. 读 summary.md
4. 先读 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger（仅作辅助记忆；如果与 available-combos.md / experiment-facts.md 冲突，以 executable facts 为准）
5. 再读 knowledge-base.md（特别是 Pending Work 和 Frontier Coverage）
6. 再读 available-combos.md
7. 必要时读 experiments/ 验证细节

强约束：
- 只允许从 ## Ready Combos 里追加新任务
- 不允许重复已经完成或已经确认 blocked 的组合
- 只要任务命中 executable facts 明确拒绝的 frontier，就必须丢弃；summary.md 里的 Do Not Retry This Cycle 仅在与 executable facts 一致时成立；如果一定要重提，reason 必须说明 state changed
- engine_params 只能使用 device-profile.md 里列出的 Tunable Params；不要设置 host port、container name、runtime、image 等宿主资源字段
- DC-2: search_space 的 value 必须是 JSON 数组（单元素 = 固定值；多元素 = 实际 sweep 维度）。不要把 scalar engine_params 伪装成 tuning
- DC-3: benchmark block 里的 concurrency / input_tokens / max_tokens 若出现必须是非空数组；不指定负载就省略整个 benchmark block
- tune 任务如果要做真实参数搜索，必须填写 search_space；需要固定参数时，把它写成 search_space 里的单元素数组
- 你的 plan.md 只写下一轮新增任务，不要把旧任务原样重抄
- 你必须通过工具修订 plan.md 并调用 done()

修订目标：
- 最多追加 {max_tasks} 个任务
- 针对 summary.md 里的具体发现行动，并明确说明它对应的 Ready Combo 和 blocker 规避理由
- 优先消费 knowledge-base.md 里的 Pending Work，再考虑新的探索分支
- 优先解决真实 bug、缩小可行区间、或验证候选 golden config
- 如果仍有未在 Recent History 里出现的 Ready 模型，至少保留一个任务给这些模型；不要把整轮都花在同一个已探索模型族的继续 pivot 上
- 当存在其他未探索 Ready 模型或 family 时，同一 model family 最多追加 2 个任务
- 保留 plan.md 的结构：## Objective / ## Fact Snapshot / ## Task Board / ## Tasks`
