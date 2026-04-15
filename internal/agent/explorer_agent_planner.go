package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ExplorerAgentPlanner implements Planner using a tool-calling agent loop.
type ExplorerAgentPlanner struct {
	llm       LLMClient
	workspace *ExplorerWorkspace
	tools     *ExplorerToolExecutor
	queryFn   QueryFunc
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

	messages := []Message{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: phaseUserPrompt(phase)},
	}

	var totalTokens int
	maxTurns := 30

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
		return "开始 plan 阶段。先读 index.md、available-combos.md、knowledge-base.md、experiment-facts.md，以及 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger；只能从 Ready Combos 选任务，且必须避开当前循环的阻塞和拒绝重试项；如果 knowledge-base.md 的 Frontier Coverage 里存在最近未探索模型，优先给这些模型留任务，再考虑对已探索模型做新的引擎 pivot；不要发明标准镜像、隐藏变体或不在 Ready Combos 里的任务；完成后写入完整的 plan.md 并调用 done()。"
	case "check":
		return "开始 check 阶段。先读 experiment-facts.md，再读取实验结果与 summary.md；逐个回填 experiments/*.md 的 Agent Notes（每个 3-5 行分析），再更新 summary.md（含横向对比表和场景标注的 Recommended Configurations）；Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger 写成结构化内容；如果 summary.md 与 experiment-facts.md 冲突，以 experiment-facts.md 为准；用 done(verdict) 结束。"
	case "act":
		return "开始 act 阶段。先读 experiment-facts.md，再基于 summary.md 修订 plan.md；只能追加新的 Ready Combos 任务，且不得命中 Do Not Retry This Cycle；如果上一轮事实已经表明某 combo blocked 或不在 Ready Combos，就不要重提；调用 done()。"
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
		filtered = append(filtered, task)
	}
	return filtered
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

func hasBenchmarkEvidence(perf PerfSummary) bool {
	return perf.ThroughputTPS > 0 && perf.LatencyP50Ms > 0
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
		for _, bench := range rec.Result.Benchmarks {
			if bench.ThroughputTPS > 0 {
				evidence[key] = true
				break
			}
		}
	}
	for _, cfg := range configs {
		key := strings.ToLower(strings.TrimSpace(cfg.Model)) + "|" + strings.ToLower(strings.TrimSpace(cfg.Engine))
		switch strings.ToLower(strings.TrimSpace(cfg.Confidence)) {
		case "", "provisional":
			continue
		case "tuned":
			if !hasBenchmarkEvidence(cfg.Performance) {
				return fmt.Errorf("summary recommendation %s/%s marked tuned without benchmark evidence", cfg.Model, cfg.Engine)
			}
			if len(records) > 0 && !evidence[key] {
				return fmt.Errorf("summary recommendation %s/%s marked tuned without matching successful experiment", cfg.Model, cfg.Engine)
			}
		case "validated":
			if !hasBenchmarkEvidence(cfg.Performance) {
				return fmt.Errorf("summary recommendation %s/%s marked validated without benchmark evidence", cfg.Model, cfg.Engine)
			}
			if len(records) > 0 && !evidence[key] {
				return fmt.Errorf("summary recommendation %s/%s marked validated without matching successful experiment", cfg.Model, cfg.Engine)
			}
		default:
			return fmt.Errorf("summary recommendation %s/%s has unknown confidence %q", cfg.Model, cfg.Engine, cfg.Confidence)
		}
	}
	return nil
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
			feedback := fmt.Sprintf("\n\n## Validation Guard Feedback\n\n⚠️ %s\n\nDo NOT use `validated` or `tuned` confidence when throughput_tps is 0 or null. Downgrade to `provisional`.\n", err.Error())
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
		Kind:      ts.Kind,
		Hardware:  defaultHardware,
		Model:     ts.Model,
		Engine:    ts.Engine,
		Params:    params,
		Benchmark: ts.Benchmark,
		Reason:    ts.Reason,
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
3. 读 knowledge-base.md（特别是 Frontier Coverage）
4. 读 experiment-facts.md
5. 必要时再读 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger
6. 必要时再读 device-profile.md、experiments/

强约束：
- index.md 里的规则是最高优先级
- 只有 available-combos.md 中 ## Ready Combos 的组合，才允许出现在新任务里
- 如果组合在 ## Blocked Combos，或者根本不在 ## Ready Combos，就视为不可执行
- summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle 是本轮硬约束；如果要重试被阻塞的 family，reason 必须说明 state changed
- engine_params 只能使用 device-profile.md 里列出的 Tunable Params；不要设置 host port、container name、runtime、image 等宿主资源字段
- 不要根据常识脑补默认引擎、标准镜像、隐藏模型变体
- query 工具只支持 search、compare、gaps、aggregate
- 你必须通过工具写 plan.md 并调用 done()；不要只输出自然语言

计划目标：
- 每轮最多 {max_tasks} 个任务
- 优先选择最有信息增益、且真实可执行的 Ready Combos
- 先做 validate，只有已有 baseline 或明确理由时才做 tune
- 保持任务多样性，但不要浪费在重复失败组合上
- 如果存在“未出现在 Recent History 的 Ready 模型”，至少先给其中一个模型分配任务，再考虑继续围绕最近已探索模型做跨引擎 pivot
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
5. 读 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger
6. 读 summary.md 和 knowledge-base.md
7. 逐个回填本轮 experiments/*.md 的 ## Agent Notes（用 append_to_file）
8. 更新 summary.md（含横向对比表）并调用 done(verdict)

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
- 只有在下一轮仍有高价值 Ready Combos 时，才返回 verdict=”continue”
- **环境性失败判定**：如果本轮所有失败都属于环境/基础设施问题（Docker pull 失败、网络超时、端口冲突、镜像不存在、容器启动崩溃、OOM kill、驱动不兼容等），且没有新的可行 Ready Combos 能绕过这些环境问题，必须返回 verdict=”done”。重试不会修复环境问题，继续只会浪费 token。
- **绝对禁止**：当 throughput_tps 为 0、null 或缺失时，confidence 不允许写 validated 或 tuned，必须写 provisional。这是硬约束，系统会自动拦截违规。
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
    latency_p50_ms: <float>
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
4. 先读 summary.md 里的 Confirmed Blockers / Do Not Retry This Cycle / Evidence Ledger
5. 再读 knowledge-base.md（特别是 Frontier Coverage）
6. 再读 available-combos.md
7. 必要时读 experiments/ 验证细节

强约束：
- 只允许从 ## Ready Combos 里追加新任务
- 不允许重复已经完成或已经确认 blocked 的组合
- 只要任务命中 Do Not Retry This Cycle，就必须丢弃；如果一定要重提，reason 必须说明 state changed
- engine_params 只能使用 device-profile.md 里列出的 Tunable Params；不要设置 host port、container name、runtime、image 等宿主资源字段
- 你的 plan.md 只写下一轮新增任务，不要把旧任务原样重抄
- 你必须通过工具修订 plan.md 并调用 done()

修订目标：
- 最多追加 {max_tasks} 个任务
- 针对 summary.md 里的具体发现行动，并明确说明它对应的 Ready Combo 和 blocker 规避理由
- 优先解决真实 bug、缩小可行区间、或验证候选 golden config
- 如果仍有未在 Recent History 里出现的 Ready 模型，至少保留一个任务给这些模型；不要把整轮都花在同一个已探索模型族的继续 pivot 上
- 保留 plan.md 的结构：## Objective / ## Fact Snapshot / ## Task Board / ## Tasks`
