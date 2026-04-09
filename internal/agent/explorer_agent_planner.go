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
		if streamer, ok := p.llm.(StreamingLLMClient); ok {
			resp, err = streamer.ChatCompletionStream(ctx, messages, toolDefs, func(delta CompletionDelta) {})
		} else {
			resp, err = p.llm.ChatCompletion(ctx, messages, toolDefs)
		}
		if err != nil {
			return totalTokens, fmt.Errorf("LLM call in %s phase (turn %d): %w", phase, turn, err)
		}
		totalTokens += resp.TotalTokens

		if len(resp.ToolCalls) == 0 {
			slog.Info("explorer agent: phase ended (no tool calls)", "phase", phase, "turn", turn)
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

// Plan implements Planner: refreshes fact docs, runs Plan phase, parses tasks.
func (p *ExplorerAgentPlanner) Plan(ctx context.Context, input PlanInput) (*ExplorerPlan, int, error) {
	if err := p.workspace.Init(); err != nil {
		return nil, 0, fmt.Errorf("init workspace: %w", err)
	}

	if err := p.workspace.RefreshFactDocuments(input); err != nil {
		return nil, 0, fmt.Errorf("refresh fact docs: %w", err)
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

	if len(tasks) > p.maxTasks {
		tasks = tasks[:p.maxTasks]
	}

	planTasks := make([]PlanTask, len(tasks))
	for i, ts := range tasks {
		planTasks[i] = taskSpecToPlanTask(ts, input.Hardware.Profile)
	}

	if len(planTasks) == 0 {
		return nil, tokens, fmt.Errorf("agent produced no tasks")
	}

	plan := &ExplorerPlan{
		ID:        generatePlanID(),
		Tier:      2,
		Tasks:     planTasks,
		Reasoning: "agent-planned",
	}
	return plan, tokens, nil
}

// Analyze runs Check phase (+ optional Act phase if verdict="continue").
func (p *ExplorerAgentPlanner) Analyze(ctx context.Context) (string, []TaskSpec, int, error) {
	tokens, err := p.runPhase(ctx, "check", checkPhaseSystemPrompt)
	if err != nil {
		return "", nil, tokens, fmt.Errorf("check phase: %w", err)
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

	return verdict, extraTasks, tokens, nil
}

func taskSpecToPlanTask(ts TaskSpec, defaultHardware string) PlanTask {
	params := make(map[string]any)
	for k, v := range ts.EngineParams {
		params[k] = v
	}
	return PlanTask{
		Kind:     ts.Kind,
		Hardware: defaultHardware,
		Model:    ts.Model,
		Engine:   ts.Engine,
		Params:   params,
		Reason:   ts.Reason,
	}
}

func generatePlanID() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d", timeNow().UnixNano())))
	return fmt.Sprintf("%x", h)[:8]
}

var timeNow = time.Now

const planPhaseSystemPrompt = `你是一个 AI 推理优化研究员，负责在边缘设备上探索最佳的模型+引擎配置。

你的工作环境是一个文档工作区（~/.aima/explorer/），包含：
- device-profile.md — 设备硬件、已安装模型和引擎的完整信息
- available-combos.md — 经过兼容性过滤的可行 model×engine 组合
- knowledge-base.md — 已有的知识库（历史记录、中央 advisory、引擎能力）
- summary.md — 你之前积累的发现和策略（可能为空）
- experiments/ — 历次实验的详细报告

你的目标：制定一个探索计划，选择最有价值的实验来扩展对这台设备的推理能力认知。

工作流程：
1. 用 cat 读取关键文档，了解设备状态和已有知识
2. 用 ls/grep 按需查看实验历史
3. 用 query 深入查询知识库细节（如需）
4. 思考策略，用 write 写入 plan.md
5. plan.md 的 ## Tasks 下必须包含一个 yaml code block，定义具体任务
6. 用 done() 通知系统你已完成规划

计划原则：
- 每轮最多 {max_tasks} 个任务
- 优先覆盖未测试的 model+engine 组合（breadth first）
- 已有 baseline 的模型才可以 tune
- 考虑引擎特性选择参数（如 MoE 模型用支持 cpu_gpu_hybrid 的引擎）
- 为每个任务设计合理的 benchmark 参数（concurrency、input_tokens、max_tokens）
- reason 字段要说明为什么这个实验有价值

Task YAML 格式：
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

const checkPhaseSystemPrompt = `你是一个 AI 推理实验分析师。刚刚完成了一轮探索实验，你需要分析结果并更新知识。

工作区状态：
- plan.md — 本轮执行的计划
- experiments/ — 新产生的实验报告（含 benchmark matrix 数据）
- summary.md — 之前的发现和策略
- knowledge-base.md — 已有知识库

你的任务：
1. 用 cat 读取新产生的实验报告
2. 分析结果：哪些成功了、性能如何、有没有意外发现
3. 对比已有知识（knowledge-base.md）：新结果是否优于已知最佳配置
4. 用 write/append 更新 summary.md：
   - ## Key Findings 追加新发现
   - ## Recommended Configurations 的 yaml block 更新推荐配置
   - ## Current Strategy 更新策略
5. 可选：用 append 为实验文件补充 ## Agent Notes
6. 用 done(verdict) 通知系统：
   - verdict="done" — 本轮目标达成，无需追加实验
   - verdict="continue" — 发现需要追加/重试的实验

Recommended Configurations YAML 格式：
- model: <name>
  engine: <engine>
  hardware: <profile>
  engine_params: { ... }
  performance:
    throughput_tps: <float>
    latency_p50_ms: <float>
  confidence: validated|tuned|provisional
  note: "<string>"`

const actPhaseSystemPrompt = `你是一个 AI 推理实验规划师。根据上一轮实验的分析结果，你决定追加实验。

工作区中 summary.md 已更新了最新分析。请：
1. 读取 summary.md 了解分析结论
2. 读取 available-combos.md 确认可行组合
3. 修订 plan.md，在 ## Tasks 的 yaml block 中只写追加的新任务
4. 用 done() 通知系统

修订原则：
- 只追加新任务，不重复已完成的实验
- 针对具体发现做针对性调整（如降低 gmu、换引擎、调 TP）
- 最多追加 {max_tasks} 个任务`
