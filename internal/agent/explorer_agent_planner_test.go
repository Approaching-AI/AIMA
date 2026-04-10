package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// mockStreamingLLM is a test double that returns pre-scripted responses.
type mockStreamingLLM struct {
	responses   []Response
	callIndex   int
	calls       [][]Message
	chatCalls   int
	streamCalls int
}

func (m *mockStreamingLLM) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	m.chatCalls++
	m.calls = append(m.calls, messages)
	if m.callIndex >= len(m.responses) {
		return &Response{Content: ""}, nil
	}
	resp := m.responses[m.callIndex]
	m.callIndex++
	return &resp, nil
}

func (m *mockStreamingLLM) ChatCompletionStream(ctx context.Context, messages []Message, tools []ToolDefinition, onDelta func(CompletionDelta)) (*Response, error) {
	m.streamCalls++
	return m.ChatCompletion(ctx, messages, tools)
}

func TestRunPhase_PlanWritesAndDone(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planContent := `# Exploration Plan

## Strategy
Test combo.

## Tasks
` + "```yaml\n" + `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"
` + "```\n"

	mock := &mockStreamingLLM{
		responses: []Response{
			{ToolCalls: []ToolCall{
				{ID: "1", Name: "cat", Arguments: `{"path":"device-profile.md"}`},
			}},
			{ToolCalls: []ToolCall{
				{ID: "2", Name: "write", Arguments: `{"path":"plan.md","content":` + jsonEscape(planContent) + `}`},
			}},
			{ToolCalls: []ToolCall{
				{ID: "3", Name: "done", Arguments: `{}`},
			}},
		},
	}

	_ = ws.writeFactDocument("device-profile.md", "# Device\n## Hardware\n- GPU: test\n")

	planner := &ExplorerAgentPlanner{
		llm:       mock,
		workspace: ws,
		maxTasks:  5,
		maxCycles: 3,
	}

	tokens, err := planner.runPhase(context.Background(), "plan", "test system prompt")
	if err != nil {
		t.Fatalf("runPhase: %v", err)
	}
	if tokens < 0 {
		t.Errorf("tokens=%d", tokens)
	}

	tasks, err := ws.ParsePlan()
	if err != nil {
		t.Fatalf("ParsePlan: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Model != "test-model" {
		t.Errorf("tasks: %+v", tasks)
	}
}

func TestRunPhase_PrefersStreamingAndAddsUserMessage(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planContent := `# Exploration Plan

## Strategy
Read facts and plan one task.

## Tasks
` + "```yaml\n" + `- kind: validate
  model: test-model
  engine: vllm
  engine_params: {}
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"
` + "```\n"

	mock := &mockStreamingLLM{
		responses: []Response{
			{Content: planContent},
		},
	}

	planner := NewExplorerAgentPlanner(mock, ws)
	if _, err := planner.runPhase(context.Background(), "plan", "system prompt"); err != nil {
		t.Fatalf("runPhase: %v", err)
	}

	if mock.streamCalls != 1 {
		t.Fatalf("streamCalls=%d, want 1", mock.streamCalls)
	}
	if mock.chatCalls != 1 {
		t.Fatalf("chatCalls=%d, want 1 delegated stream call", mock.chatCalls)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("calls=%d, want 1", len(mock.calls))
	}
	if got := len(mock.calls[0]); got != 2 {
		t.Fatalf("message count=%d, want 2", got)
	}
	if mock.calls[0][0].Role != "system" {
		t.Fatalf("first role=%q, want system", mock.calls[0][0].Role)
	}
	if mock.calls[0][1].Role != "user" {
		t.Fatalf("second role=%q, want user", mock.calls[0][1].Role)
	}
	if mock.calls[0][1].Content == "" {
		t.Fatal("user message content is empty")
	}
	if !containsAll(mock.calls[0][1].Content, "index.md", "Ready Combos") {
		t.Fatalf("user message missing grounding hints: %q", mock.calls[0][1].Content)
	}
}

func TestPhasePromptsReferenceStructuredMemory(t *testing.T) {
	if !containsAll(planPhaseSystemPrompt, "Confirmed Blockers", "Do Not Retry This Cycle", "Evidence Ledger", "Ready Combos") {
		t.Fatalf("plan prompt missing structured-memory guidance: %q", planPhaseSystemPrompt)
	}
	if !containsAll(checkPhaseSystemPrompt, "Confirmed Blockers", "Do Not Retry This Cycle", "Evidence Ledger", "validated|tuned|provisional") {
		t.Fatalf("check prompt missing structured-memory guidance: %q", checkPhaseSystemPrompt)
	}
	if !containsAll(actPhaseSystemPrompt, "Confirmed Blockers", "Do Not Retry This Cycle", "Evidence Ledger", "Ready Combos") {
		t.Fatalf("act prompt missing structured-memory guidance: %q", actPhaseSystemPrompt)
	}
}

// jsonEscape returns a JSON string literal for content.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestAgentPlannerPlan(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planYAML := `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"`

	planContent := "# Exploration Plan\n\n## Strategy\nTest.\n\n## Tasks\n```yaml\n" + planYAML + "\n```\n"

	mock := &mockStreamingLLM{
		responses: []Response{
			{ToolCalls: []ToolCall{{ID: "1", Name: "cat", Arguments: `{"path":"device-profile.md"}`}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "write", Arguments: `{"path":"plan.md","content":` + jsonEscape(planContent) + `}`}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "done", Arguments: `{}`}}},
		},
	}

	input := PlanInput{
		Hardware:     HardwareInfo{Profile: "nvidia-rtx4090-x86", GPUArch: "Ada", GPUCount: 2, VRAMMiB: 49140},
		LocalModels:  []LocalModel{{Name: "test-model", Format: "safetensors", Type: "llm", SizeBytes: 5_000_000_000}},
		LocalEngines: []LocalEngine{{Name: "vllm", Type: "vllm", Runtime: "container"}},
	}

	planner := NewExplorerAgentPlanner(mock, ws)
	plan, tokens, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if tokens < 0 {
		t.Logf("tokens=%d (mock returns 0, ok)", tokens)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(plan.Tasks))
	}
	if plan.Tasks[0].Model != "test-model" {
		t.Errorf("task model=%s", plan.Tasks[0].Model)
	}
	if plan.Tier != 2 {
		t.Errorf("tier=%d", plan.Tier)
	}
}

func TestAgentPlannerPlan_AssistantOnlyContentFallback(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planYAML := `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "test"`

	planContent := "# Exploration Plan\n\n## Strategy\nAssistant-only fallback.\n\n## Tasks\n```yaml\n" + planYAML + "\n```\n"

	mock := &mockStreamingLLM{
		responses: []Response{
			{Content: planContent},
		},
	}

	input := PlanInput{
		Hardware:     HardwareInfo{Profile: "nvidia-rtx4090-x86", GPUArch: "Ada", GPUCount: 2, VRAMMiB: 49140},
		LocalModels:  []LocalModel{{Name: "test-model", Format: "safetensors", Type: "llm", SizeBytes: 5_000_000_000}},
		LocalEngines: []LocalEngine{{Name: "vllm", Type: "vllm", Runtime: "container"}},
	}

	planner := NewExplorerAgentPlanner(mock, ws)
	plan, _, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(plan.Tasks))
	}
	if plan.Tasks[0].Model != "test-model" {
		t.Errorf("task model=%s", plan.Tasks[0].Model)
	}
	indexMD, err := ws.ReadFile("index.md")
	if err != nil {
		t.Fatalf("read index.md: %v", err)
	}
	if !containsAll(indexMD, "Source Of Truth", "Ready Combos") {
		t.Fatalf("index.md missing authority guidance: %q", indexMD)
	}
}

func TestAgentPlannerAnalyze(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	summaryContent := `# Exploration Summary

## Key Findings
- vllm works well

## Recommended Configurations
` + "```yaml\n" + `- model: test-model
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params: {}
  performance:
    throughput_tps: 100.0
    latency_p50_ms: 40
  confidence: validated
  note: "good"
` + "```\n" + `
## Current Strategy
Done for now.
`

	mock := &mockStreamingLLM{
		responses: []Response{
			{ToolCalls: []ToolCall{{ID: "1", Name: "cat", Arguments: `{"path":"experiments/001-test-model-vllm.md"}`}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "write", Arguments: `{"path":"summary.md","content":` + jsonEscape(summaryContent) + `}`}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "done", Arguments: `{"verdict":"done"}`}}},
		},
	}

	_ = ws.writeFactDocument("experiments/001-test-model-vllm.md", "# Experiment\n## Result\nstatus: completed\n")

	planner := NewExplorerAgentPlanner(mock, ws)
	verdict, extraTasks, _, err := planner.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if verdict != "done" {
		t.Errorf("verdict=%s", verdict)
	}
	if len(extraTasks) != 0 {
		t.Errorf("extraTasks=%d (expected 0 for verdict=done)", len(extraTasks))
	}
}

func TestAgentPlannerAnalyze_AssistantOnlyContentDefaultsDone(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	summaryContent := `# Exploration Summary

## Key Findings
- vllm works well

## Recommended Configurations
` + "```yaml\n" + `- model: test-model
  engine: vllm
  hardware: nvidia-rtx4090-x86
  engine_params: {}
  performance:
    throughput_tps: 100.0
    latency_p50_ms: 40
  confidence: validated
  note: "good"
` + "```\n" + `
## Current Strategy
Done for now.
`

	mock := &mockStreamingLLM{
		responses: []Response{
			{Content: summaryContent},
		},
	}

	planner := NewExplorerAgentPlanner(mock, ws)
	verdict, extraTasks, _, err := planner.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if verdict != "done" {
		t.Errorf("verdict=%s, want done", verdict)
	}
	if len(extraTasks) != 0 {
		t.Errorf("extraTasks=%d (expected 0 for verdict=done)", len(extraTasks))
	}
	configs, err := ws.ExtractRecommendations()
	if err != nil {
		t.Fatalf("ExtractRecommendations: %v", err)
	}
	if len(configs) != 1 || configs[0].Model != "test-model" {
		t.Errorf("recommendations: %+v", configs)
	}
}

func TestFullPDCACycle(t *testing.T) {
	dir := t.TempDir()
	ws := NewExplorerWorkspace(dir)
	_ = ws.Init()

	planYAML := `- kind: validate
  model: test-model
  engine: vllm
  engine_params:
    gpu_memory_utilization: 0.90
  benchmark:
    concurrency: [1]
    input_tokens: [128]
    max_tokens: [256]
    requests_per_combo: 3
  reason: "first test"`

	planContent := "# Exploration Plan\n\n## Strategy\nTest.\n\n## Tasks\n```yaml\n" + planYAML + "\n```\n"

	summaryContent := `# Exploration Summary

## Key Findings
- works

## Recommended Configurations
` + "```yaml\n" + `- model: test-model
  engine: vllm
  hardware: test-hw
  engine_params: {}
  performance:
    throughput_tps: 100.0
    latency_p50_ms: 40
  confidence: validated
  note: "ok"
` + "```\n" + `
## Current Strategy
Done.
`

	mock := &mockStreamingLLM{
		responses: []Response{
			// Plan phase
			{ToolCalls: []ToolCall{{ID: "1", Name: "cat", Arguments: `{"path":"device-profile.md"}`}}},
			{ToolCalls: []ToolCall{{ID: "2", Name: "write", Arguments: `{"path":"plan.md","content":` + jsonEscape(planContent) + `}`}}},
			{ToolCalls: []ToolCall{{ID: "3", Name: "done", Arguments: `{}`}}},
			// Check phase
			{ToolCalls: []ToolCall{{ID: "4", Name: "ls", Arguments: `{"path":"experiments"}`}}},
			{ToolCalls: []ToolCall{{ID: "5", Name: "write", Arguments: `{"path":"summary.md","content":` + jsonEscape(summaryContent) + `}`}}},
			{ToolCalls: []ToolCall{{ID: "6", Name: "done", Arguments: `{"verdict":"done"}`}}},
		},
	}

	input := PlanInput{
		Hardware:     HardwareInfo{Profile: "test-hw", GPUArch: "Ada", GPUCount: 2, VRAMMiB: 49140},
		LocalModels:  []LocalModel{{Name: "test-model", Format: "safetensors", Type: "llm", SizeBytes: 5e9}},
		LocalEngines: []LocalEngine{{Name: "vllm", Type: "vllm", Runtime: "container"}},
	}

	planner := NewExplorerAgentPlanner(mock, ws)

	// 1. Plan
	plan, _, err := planner.Plan(context.Background(), input)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Tasks) != 1 {
		t.Fatalf("plan tasks=%d", len(plan.Tasks))
	}

	// Simulate Do: write experiment result
	_, _ = ws.WriteExperimentResult(1, TaskSpec{
		Kind: "validate", Model: "test-model", Engine: "vllm",
	}, ExperimentResult{Status: "completed", Benchmarks: []BenchmarkEntry{
		{Concurrency: 1, InputTokens: 128, MaxTokens: 256, ThroughputTPS: 100},
	}})

	// 2. Check
	verdict, extra, _, err := planner.Analyze(context.Background())
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if verdict != "done" {
		t.Errorf("verdict=%s", verdict)
	}
	if len(extra) != 0 {
		t.Errorf("extra tasks=%d", len(extra))
	}

	// 3. Verify summary.md has recommendations
	configs, _ := ws.ExtractRecommendations()
	if len(configs) != 1 || configs[0].Model != "test-model" {
		t.Errorf("recommendations: %+v", configs)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
