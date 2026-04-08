package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	state "github.com/jguan/aima/internal"
)

// mockLLM is a test double that returns predefined responses in sequence.
type mockLLM struct {
	responses []*Response
	calls     int
	messages  [][]Message // record of all calls
}

func (m *mockLLM) ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error) {
	m.messages = append(m.messages, messages)
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("no more mock responses (call %d)", m.calls)
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

// mockTools is a test double for ToolExecutor.
type mockTools struct {
	tools   []ToolDefinition
	results map[string]*ToolResult
	calls   []string // record of tool calls
	execute func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error)
}

func (m *mockTools) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
	m.calls = append(m.calls, name)
	if m.execute != nil {
		return m.execute(ctx, name, arguments)
	}
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *mockTools) ListTools() []ToolDefinition {
	return m.tools
}

func TestAgent_SimpleQuery(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{
			{Content: "Hello! I can help with that."},
		},
	}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "hardware.detect", Description: "Detect hardware"},
		},
	}

	agent := NewAgent(llm, tools)
	result, _, _, err := agent.Ask(context.Background(), "", "Hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "Hello! I can help with that." {
		t.Errorf("result = %q, want greeting", result)
	}
	if llm.calls != 1 {
		t.Errorf("llm calls = %d, want 1", llm.calls)
	}
}

func TestAgent_SingleToolCall(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{
			// First response: request tool call
			{
				ToolCalls: []ToolCall{
					{ID: "tc1", Name: "hardware.detect", Arguments: `{}`},
				},
			},
			// Second response: final answer after seeing tool result
			{Content: "You have an NVIDIA RTX 4090 with 24GB VRAM."},
		},
	}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "hardware.detect", Description: "Detect hardware"},
		},
		results: map[string]*ToolResult{
			"hardware.detect": {Content: `{"gpu":"NVIDIA RTX 4090","vram_mb":24576}`},
		},
	}

	agent := NewAgent(llm, tools)
	result, _, _, err := agent.Ask(context.Background(), "", "What GPU do I have?")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "You have an NVIDIA RTX 4090 with 24GB VRAM." {
		t.Errorf("unexpected result: %q", result)
	}
	if llm.calls != 2 {
		t.Errorf("llm calls = %d, want 2", llm.calls)
	}
	if len(tools.calls) != 1 || tools.calls[0] != "hardware.detect" {
		t.Errorf("tool calls = %v, want [hardware.detect]", tools.calls)
	}
}

func TestAgent_MultiTurnToolCalling(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{
			// Turn 1: call hardware.detect
			{ToolCalls: []ToolCall{{ID: "tc1", Name: "hardware.detect", Arguments: `{}`}}},
			// Turn 2: call model.list
			{ToolCalls: []ToolCall{{ID: "tc2", Name: "model.list", Arguments: `{}`}}},
			// Turn 3: call deploy.apply
			{ToolCalls: []ToolCall{{ID: "tc3", Name: "deploy.apply", Arguments: `{"model":"qwen3-8b"}`}}},
			// Turn 4: final answer
			{Content: "Deployed qwen3-8b on your RTX 4090."},
		},
	}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "hardware.detect", Description: "Detect hardware"},
			{Name: "model.list", Description: "List models"},
			{Name: "deploy.apply", Description: "Deploy"},
		},
		results: map[string]*ToolResult{
			"hardware.detect": {Content: `{"gpu":"RTX 4090"}`},
			"model.list":      {Content: `["qwen3-8b","glm-4"]`},
			"deploy.apply":    {Content: `{"status":"ok"}`},
		},
	}

	agent := NewAgent(llm, tools)
	result, _, _, err := agent.Ask(context.Background(), "", "Deploy the best model")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "Deployed qwen3-8b on your RTX 4090." {
		t.Errorf("unexpected result: %q", result)
	}
	if llm.calls != 4 {
		t.Errorf("llm calls = %d, want 4", llm.calls)
	}
	if len(tools.calls) != 3 {
		t.Errorf("tool calls = %d, want 3", len(tools.calls))
	}
}

func TestAgent_MaxTurnsExceeded(t *testing.T) {
	// LLM always returns tool calls, never a final answer
	infinite := make([]*Response, 100)
	for i := range infinite {
		infinite[i] = &Response{
			ToolCalls: []ToolCall{{ID: fmt.Sprintf("tc%d", i), Name: "hardware.detect", Arguments: `{}`}},
		}
	}
	llm := &mockLLM{responses: infinite}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "hardware.detect", Description: "Detect hardware"},
		},
		results: map[string]*ToolResult{
			"hardware.detect": {Content: `{}`},
		},
	}

	agent := NewAgent(llm, tools, WithMaxTurns(3))
	_, _, _, err := agent.Ask(context.Background(), "", "test")
	if err == nil {
		t.Fatal("expected error for max turns exceeded")
	}
	if llm.calls != 3 {
		t.Errorf("llm calls = %d, want 3", llm.calls)
	}
}

func TestAgent_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	llm := &mockLLM{
		responses: []*Response{{Content: "should not get here"}},
	}
	tools := &mockTools{}

	agent := NewAgent(llm, tools)
	_, _, _, err := agent.Ask(ctx, "", "test")
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestAgent_ToolExecutionError(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{
			// Request a tool that will fail
			{ToolCalls: []ToolCall{{ID: "tc1", Name: "missing.tool", Arguments: `{}`}}},
			// After seeing error, return final answer
			{Content: "That tool is not available."},
		},
	}
	tools := &mockTools{
		tools:   []ToolDefinition{},
		results: map[string]*ToolResult{}, // no results → ExecuteTool returns error
	}

	agent := NewAgent(llm, tools)
	result, _, _, err := agent.Ask(context.Background(), "", "do something")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "That tool is not available." {
		t.Errorf("unexpected result: %q", result)
	}

	// Verify the error message was passed back to LLM
	if len(llm.messages) < 2 {
		t.Fatalf("expected at least 2 LLM calls")
	}
	lastMessages := llm.messages[1]
	foundToolError := false
	for _, m := range lastMessages {
		if m.Role == "tool" && m.ToolCallID == "tc1" {
			if m.Content != "error: tool not found: missing.tool" {
				t.Errorf("unexpected tool error content: %q", m.Content)
			}
			foundToolError = true
		}
	}
	if !foundToolError {
		t.Error("tool error message not found in conversation")
	}
}

func TestAgent_ToolResultIsError(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{
			{ToolCalls: []ToolCall{{ID: "tc1", Name: "shell.exec", Arguments: `{"command":"rm -rf /"}`}}},
			{Content: "Command was denied."},
		},
	}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "shell.exec", Description: "Execute shell command"},
		},
		results: map[string]*ToolResult{
			"shell.exec": {Content: "command not allowed", IsError: true},
		},
	}

	agent := NewAgent(llm, tools)
	result, _, _, err := agent.Ask(context.Background(), "", "delete everything")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "Command was denied." {
		t.Errorf("unexpected result: %q", result)
	}

	// Verify error prefix was added
	lastMessages := llm.messages[1]
	for _, m := range lastMessages {
		if m.Role == "tool" && m.ToolCallID == "tc1" {
			if m.Content != "error: command not allowed" {
				t.Errorf("unexpected tool message: %q", m.Content)
			}
		}
	}
}

func TestAgent_SystemPrompt(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "ok"}},
	}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "hardware.detect", Description: "Detect hardware capabilities"},
			{Name: "model.list", Description: "List models"},
		},
	}

	agent := NewAgent(llm, tools)
	agent.Ask(context.Background(), "", "test")

	if len(llm.messages) < 1 {
		t.Fatal("no LLM calls recorded")
	}
	msgs := llm.messages[0]
	if msgs[0].Role != "system" {
		t.Errorf("first message role = %q, want system", msgs[0].Role)
	}
	sysPrompt := msgs[0].Content
	if len(sysPrompt) == 0 {
		t.Fatal("system prompt is empty")
	}
	// System prompt should be the embedded core prompt
	if !contains(sysPrompt, "# AIMA Agent") {
		t.Error("system prompt missing embedded core prompt header")
	}
	if !contains(sysPrompt, "hardware.detect") {
		t.Error("system prompt missing hardware.detect tool reference")
	}
	if !contains(sysPrompt, "deploy.apply") {
		t.Error("system prompt missing deploy.apply tool reference")
	}
}

func TestCorePromptEmbedded(t *testing.T) {
	if len(corePrompt) == 0 {
		t.Fatal("corePrompt embed is empty")
	}
	if !contains(corePrompt, "# AIMA Agent") {
		t.Error("corePrompt missing header")
	}
}

func TestWithMaxTurns(t *testing.T) {
	agent := NewAgent(nil, nil, WithMaxTurns(5))
	if agent.maxTurns != 5 {
		t.Errorf("maxTurns = %d, want 5", agent.maxTurns)
	}
}

func TestDispatcher_BasicRouting(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)
	d := NewDispatcher(goAgent)

	result, _, _, err := d.Ask(context.Background(), "optimize everything", DispatchOption{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "from L3a" {
		t.Errorf("result = %q, want from L3a", result)
	}
}

func TestDispatcher_SessionContinuity(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a with session"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	store := NewSessionStore()
	goAgent := NewAgent(llm, tools, WithSessions(store))
	d := NewDispatcher(goAgent)

	result, sid, _, err := d.Ask(context.Background(), "continue", DispatchOption{SessionID: "my-session"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "from L3a with session" {
		t.Errorf("result = %q, want from L3a with session", result)
	}
	if sid != "my-session" {
		t.Errorf("sessionID = %q, want my-session", sid)
	}
}

func TestAgent_NilLLM(t *testing.T) {
	tools := &mockTools{tools: []ToolDefinition{}}
	agent := NewAgent(nil, tools)

	_, _, _, err := agent.Ask(context.Background(), "", "test")
	if err == nil {
		t.Fatal("expected error with nil LLM, not panic")
	}
	if !contains(err.Error(), "no LLM backend") {
		t.Errorf("error = %q, want mention of no LLM backend", err)
	}
}

func TestAgent_Available(t *testing.T) {
	llm := &mockLLM{responses: []*Response{{Content: "ok"}}}
	tools := &mockTools{}

	withLLM := NewAgent(llm, tools)
	if !withLLM.Available() {
		t.Error("Available() = false with LLM, want true")
	}

	withoutLLM := NewAgent(nil, tools)
	if withoutLLM.Available() {
		t.Error("Available() = true without LLM, want false")
	}
}

func TestDispatcher_NoLLM_ReturnsError(t *testing.T) {
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(nil, tools)
	d := NewDispatcher(goAgent)

	_, _, _, err := d.Ask(context.Background(), "test", DispatchOption{})
	if err == nil {
		t.Fatal("expected error when agent has no LLM")
	}
	if !contains(err.Error(), "no LLM backend") {
		t.Errorf("error = %q, want mention of no LLM backend", err)
	}
}

// --- Session-specific tests ---

func TestSession_NewSessionReturnsID(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "hello"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	store := NewSessionStore()
	agent := NewAgent(llm, tools, WithSessions(store))

	_, sid, _, err := agent.Ask(context.Background(), "", "Hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if sid == "" {
		t.Error("expected non-empty session ID")
	}
}

func TestSession_ContinuationIncludesHistory(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{
			{Content: "Which model?"},
			{Content: "Pulling Qwen3 7B now."},
		},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	store := NewSessionStore()
	agent := NewAgent(llm, tools, WithSessions(store))

	// First turn
	_, sid, _, err := agent.Ask(context.Background(), "", "Download a model")
	if err != nil {
		t.Fatalf("Ask turn 1: %v", err)
	}

	// Second turn with same session
	_, sid2, _, err := agent.Ask(context.Background(), sid, "Qwen3 7B")
	if err != nil {
		t.Fatalf("Ask turn 2: %v", err)
	}
	if sid2 != sid {
		t.Errorf("session ID changed: %q → %q", sid, sid2)
	}

	// Verify LLM received history in second call
	if len(llm.messages) < 2 {
		t.Fatalf("expected 2 LLM calls, got %d", len(llm.messages))
	}
	secondCallMsgs := llm.messages[1]
	// Should contain: system, user("Download a model"), assistant("Which model?"), user("Qwen3 7B")
	if len(secondCallMsgs) != 4 {
		t.Errorf("second call messages = %d, want 4", len(secondCallMsgs))
	}
	if secondCallMsgs[0].Role != "system" {
		t.Errorf("msg[0].Role = %q, want system", secondCallMsgs[0].Role)
	}
	if secondCallMsgs[1].Content != "Download a model" {
		t.Errorf("msg[1].Content = %q, want 'Download a model'", secondCallMsgs[1].Content)
	}
	if secondCallMsgs[2].Content != "Which model?" {
		t.Errorf("msg[2].Content = %q, want 'Which model?'", secondCallMsgs[2].Content)
	}
	if secondCallMsgs[3].Content != "Qwen3 7B" {
		t.Errorf("msg[3].Content = %q, want 'Qwen3 7B'", secondCallMsgs[3].Content)
	}
}

func TestSession_ExpiredTreatedAsNew(t *testing.T) {
	store := &SessionStore{
		sessions: make(map[string]*session),
		ttl:      1 * time.Millisecond, // expire almost immediately
		maxMsgs:  50,
	}
	store.Save("old-session", []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "old question"},
		{Role: "assistant", Content: "old answer"},
	})

	// Wait for TTL to pass
	time.Sleep(5 * time.Millisecond)

	msgs, ok := store.Get("old-session")
	if ok {
		t.Errorf("expected expired session to return false, got %d messages", len(msgs))
	}
}

func TestSession_MissedIDTreatedAsNew(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "fresh start"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	store := NewSessionStore()
	agent := NewAgent(llm, tools, WithSessions(store))

	// Use a non-existent session ID — should start fresh
	_, sid, _, err := agent.Ask(context.Background(), "nonexistent-session", "Hello")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if sid != "nonexistent-session" {
		t.Errorf("session ID = %q, want nonexistent-session", sid)
	}

	// Verify only 2 messages sent to LLM (system + user), no history
	if len(llm.messages) != 1 {
		t.Fatalf("expected 1 LLM call, got %d", len(llm.messages))
	}
	if len(llm.messages[0]) != 2 {
		t.Errorf("messages = %d, want 2 (system + user)", len(llm.messages[0]))
	}
}

func TestSessionStore_TrimToMaxMsgs(t *testing.T) {
	store := &SessionStore{
		sessions: make(map[string]*session),
		ttl:      30 * time.Minute,
		maxMsgs:  5,
	}

	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "q1"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "q2"},
		{Role: "assistant", Content: "a2"},
		{Role: "user", Content: "q3"},
		{Role: "assistant", Content: "a3"},
	}
	store.Save("sess", msgs)

	got, ok := store.Get("sess")
	if !ok {
		t.Fatal("expected session to exist")
	}
	if len(got) != 5 {
		t.Errorf("stored messages = %d, want 5", len(got))
	}
	// First should still be system prompt
	if got[0].Role != "system" {
		t.Errorf("got[0].Role = %q, want system", got[0].Role)
	}
	// Last should be the newest message
	if got[len(got)-1].Content != "a3" {
		t.Errorf("last message = %q, want a3", got[len(got)-1].Content)
	}
}

func TestAgent_NoSessionStore_StillWorks(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "works"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}

	// Agent without WithSessions — backward compatible
	agent := NewAgent(llm, tools)
	result, sid, _, err := agent.Ask(context.Background(), "", "Hi")
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "works" {
		t.Errorf("result = %q, want works", result)
	}
	if sid == "" {
		t.Error("expected non-empty session ID even without store")
	}
}

func TestGenerateID(t *testing.T) {
	id1 := GenerateID()
	id2 := GenerateID()
	if id1 == "" || id2 == "" {
		t.Error("GenerateID returned empty string")
	}
	if id1 == id2 {
		t.Error("GenerateID returned duplicate IDs")
	}
	if len(id1) != 32 {
		t.Errorf("GenerateID length = %d, want 32 hex chars", len(id1))
	}
}

// --- P0 Feature C: Patrol auto-heal tests ---

func TestExtractDeployName(t *testing.T) {
	tests := []struct {
		message string
		want    string
	}{
		{"Deployment qwen3-8b is in CrashLoopBackOff state", "qwen3-8b"},
		{"Deployment my-model-v2 is in Error state", "my-model-v2"},
		{"Deployment a is in Failed state", "a"},
		{"Some other message", ""},
		{"Deployment  is in Error state", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			got := extractDeployName(tt.message)
			if got != tt.want {
				t.Errorf("extractDeployName(%q) = %q, want %q", tt.message, got, tt.want)
			}
		})
	}
}

func TestPatrolRecentActions(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":null}`},
			"deploy.list":    {Content: `[]`},
		},
	}

	p := NewPatrol(DefaultPatrolConfig(), tools, nil)

	// Initially no actions
	actions := p.RecentActions(10)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions initially, got %d", len(actions))
	}

	// Run a cycle — no alerts means no actions
	p.RunOnce(context.Background())
	actions = p.RecentActions(10)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions after clean cycle, got %d", len(actions))
	}
}

func TestPatrolCrashAlertTriggersHealAttempt(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":null}`},
			"deploy.list":    {Content: `[{"name":"test-deploy","status":"CrashLoopBackOff"}]`},
			"deploy.logs":    {Content: `some log: CUDA out of memory`},
		},
	}

	var actionLog []PatrolAction
	healer := NewHealer(tools)
	p := NewPatrol(DefaultPatrolConfig(), tools, nil,
		WithHealer(healer),
		WithActionCallback(func(ctx context.Context, a PatrolAction) {
			actionLog = append(actionLog, a)
		}),
	)

	alerts := p.RunOnce(context.Background())

	// Should have a critical deploy_crash alert
	var hasCrash bool
	for _, a := range alerts {
		if a.Type == "deploy_crash" && a.Severity == "critical" {
			hasCrash = true
			break
		}
	}
	if !hasCrash {
		t.Fatal("expected deploy_crash alert")
	}

	// Should have recorded at least one heal action
	actions := p.RecentActions(10)
	if len(actions) == 0 {
		t.Fatal("expected at least one action from heal attempt")
	}
	if actions[0].Type != "heal" {
		t.Errorf("action type = %q, want heal", actions[0].Type)
	}

	// Callback should have been called
	if len(actionLog) == 0 {
		t.Fatal("expected action callback to be called")
	}
}

func TestPatrolWithoutHealerRecordsNotify(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":null}`},
			"deploy.list":    {Content: `[{"name":"broken","status":"Error"}]`},
		},
	}

	// No healer → should record notify action
	p := NewPatrol(DefaultPatrolConfig(), tools, nil)
	p.RunOnce(context.Background())

	actions := p.RecentActions(10)
	if len(actions) == 0 {
		t.Fatal("expected notify action when healer is nil")
	}
	if actions[0].Type != "notify" {
		t.Errorf("action type = %q, want notify", actions[0].Type)
	}
}

func TestPatrolSelfHealDisabled(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":null}`},
			"deploy.list":    {Content: `[{"name":"broken","status":"CrashLoopBackOff"}]`},
		},
	}

	config := DefaultPatrolConfig()
	config.SelfHealEnabled = false
	p := NewPatrol(config, tools, nil, WithHealer(NewHealer(tools)))
	p.RunOnce(context.Background())

	// With self-heal disabled, no actions should be taken even with alerts
	actions := p.RecentActions(10)
	if len(actions) != 0 {
		t.Errorf("expected 0 actions with SelfHealEnabled=false, got %d", len(actions))
	}
}

func TestPatrolGPUIdleRequiresConfiguredDuration(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":{"temperature_celsius":55,"utilization_percent":5,"memory_used_mib":4096,"memory_total_mib":8192}}`},
			"deploy.list":    {Content: `[]`},
		},
	}

	config := DefaultPatrolConfig()
	config.GPUIdlePct = 10
	config.GPUIdleMinutes = 15
	p := NewPatrol(config, tools, nil)

	alerts := p.RunOnce(context.Background())
	for _, alert := range alerts {
		if alert.Type == "gpu_idle" {
			t.Fatal("expected no gpu_idle alert before configured idle duration elapsed")
		}
	}

	p.mu.Lock()
	p.gpuIdleSince = time.Now().Add(-16 * time.Minute)
	p.mu.Unlock()

	alerts = p.RunOnce(context.Background())
	var hasIdle bool
	for _, alert := range alerts {
		if alert.Type == "gpu_idle" {
			hasIdle = true
			break
		}
	}
	if !hasIdle {
		t.Fatal("expected gpu_idle alert after configured idle duration elapsed")
	}
}

func TestPatrolMetricsGapResetsIdleObservation(t *testing.T) {
	call := 0
	tools := &mockTools{
		tools: []ToolDefinition{},
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "device.metrics":
				call++
				if call == 1 {
					return nil, fmt.Errorf("metrics unavailable")
				}
				return &ToolResult{Content: `{"gpu":{"temperature_celsius":55,"utilization_percent":5,"memory_used_mib":4096,"memory_total_mib":8192}}`}, nil
			case "deploy.list":
				return &ToolResult{Content: `[]`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	config := DefaultPatrolConfig()
	config.GPUIdlePct = 10
	config.GPUIdleMinutes = 15
	p := NewPatrol(config, tools, nil)

	p.mu.Lock()
	p.gpuIdleSince = time.Now().Add(-16 * time.Minute)
	p.mu.Unlock()

	alerts := p.RunOnce(context.Background())
	if len(alerts) != 0 {
		t.Fatalf("expected no alerts during metrics gap, got %d", len(alerts))
	}
	if !p.gpuIdleSince.IsZero() {
		t.Fatal("expected metrics gap to clear idle observation state")
	}

	alerts = p.RunOnce(context.Background())
	for _, alert := range alerts {
		if alert.Type == "gpu_idle" {
			t.Fatal("expected first low-utilization sample after metrics gap not to trigger gpu_idle")
		}
	}
}

func TestPatrolStatusCounters(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":{"temperature_celsius":90,"utilization_percent":80,"memory_used_mib":4096,"memory_total_mib":8192}}`},
			"deploy.list":    {Content: `[]`},
		},
	}

	p := NewPatrol(DefaultPatrolConfig(), tools, nil)
	p.RunOnce(context.Background())

	status := p.Status()
	if status.AlertCount == 0 {
		t.Error("expected at least one alert (GPU temp 90 > threshold 85)")
	}
	if status.LastRun.IsZero() {
		t.Error("expected LastRun to be set after RunOnce")
	}
}

func TestPatrolStartHonorsRuntimeIntervalChanges(t *testing.T) {
	runCh := make(chan struct{}, 4)
	tools := &mockTools{
		tools: []ToolDefinition{},
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "device.metrics":
				select {
				case runCh <- struct{}{}:
				default:
				}
				return &ToolResult{Content: `{"gpu":null}`}, nil
			case "deploy.list":
				return &ToolResult{Content: `[]`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	config := DefaultPatrolConfig()
	config.Interval = 0
	p := NewPatrol(config, tools, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	defer p.Stop()

	select {
	case <-runCh:
		t.Fatal("patrol should remain disabled while interval is 0")
	case <-time.After(60 * time.Millisecond):
	}

	p.SetInterval(10 * time.Millisecond)

	select {
	case <-runCh:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("patrol did not resume after interval was updated at runtime")
	}
}

func TestPatrolRecentActionsLimit(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"device.metrics": {Content: `{"gpu":null}`},
			"deploy.list":    {Content: `[{"name":"d1","status":"Error"},{"name":"d2","status":"Failed"}]`},
		},
	}

	// No healer → generates notify actions for each crash
	p := NewPatrol(DefaultPatrolConfig(), tools, nil)
	p.RunOnce(context.Background())

	all := p.RecentActions(100)
	limited := p.RecentActions(1)
	if len(limited) != 1 {
		t.Errorf("RecentActions(1) = %d, want 1", len(limited))
	}
	if len(all) < 2 {
		t.Errorf("RecentActions(100) = %d, want >= 2", len(all))
	}
	// Limited should return the most recent
	if len(all) > 0 && len(limited) > 0 && limited[0].AlertID != all[len(all)-1].AlertID {
		t.Error("RecentActions(1) should return the most recent action")
	}
}

func TestHealerDiagnoseOOM(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"deploy.logs": {Content: `Error: torch.cuda.OutOfMemoryError: tried to allocate 2.00 GiB`},
		},
	}

	healer := NewHealer(tools)
	diag, err := healer.Diagnose(context.Background(), "test-deploy")
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if diag.Type != "oom" {
		t.Errorf("diagnosis type = %q, want oom", diag.Type)
	}
	if diag.Remedy != "reduce_gmu" {
		t.Errorf("remedy = %q, want reduce_gmu", diag.Remedy)
	}
}

func TestHealerDiagnoseUnknown(t *testing.T) {
	tools := &mockTools{
		tools: []ToolDefinition{},
		results: map[string]*ToolResult{
			"deploy.logs": {Content: `some random log output without known patterns`},
		},
	}

	healer := NewHealer(tools)
	diag, err := healer.Diagnose(context.Background(), "test-deploy")
	if err != nil {
		t.Fatalf("Diagnose: %v", err)
	}
	if diag.Type != "unknown" {
		t.Errorf("diagnosis type = %q, want unknown", diag.Type)
	}
	if diag.Remedy != "escalate" {
		t.Errorf("remedy = %q, want escalate", diag.Remedy)
	}
}

func TestHealerHealOOMUsesMatchingDeployment(t *testing.T) {
	var redeployArgs map[string]any

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.list":
				return &ToolResult{Content: `[
						{"name":"other-deploy","labels":{"aima.dev/model":"other-model","aima.dev/engine":"vllm"}},
						{"name":"target-deploy","labels":{"aima.dev/model":"qwen3-8b","aima.dev/engine":"sglang","aima.dev/slot":"slot-b"}}
					]`}, nil
			case "deploy.dry_run":
				return &ToolResult{Content: `{"engine":"sglang","slot":"slot-b","config":{"mem_fraction_static":0.8,"max_running_requests":16}}`}, nil
			case "deploy.apply":
				if err := json.Unmarshal(arguments, &redeployArgs); err != nil {
					t.Fatalf("unmarshal redeploy args: %v", err)
				}
				return &ToolResult{Content: `{"status":"ok"}`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	healer := NewHealer(tools)
	action, err := healer.Heal(context.Background(), "target-deploy", &Diagnosis{Type: "oom"})
	if err != nil {
		t.Fatalf("Heal: %v", err)
	}
	if !action.Success {
		t.Fatalf("expected successful heal action, got %+v", action)
	}
	if redeployArgs["model"] != "qwen3-8b" {
		t.Fatalf("redeploy model = %v, want qwen3-8b", redeployArgs["model"])
	}
	if redeployArgs["engine"] != "sglang" {
		t.Fatalf("redeploy engine = %v, want sglang", redeployArgs["engine"])
	}
	if redeployArgs["slot"] != "slot-b" {
		t.Fatalf("redeploy slot = %v, want slot-b", redeployArgs["slot"])
	}
	config, ok := redeployArgs["config"].(map[string]any)
	if !ok {
		t.Fatalf("redeploy config missing: %#v", redeployArgs)
	}
	if _, exists := config["config_overrides"]; exists {
		t.Fatalf("redeploy used legacy config_overrides field: %#v", config)
	}
	got, ok := config["mem_fraction_static"].(float64)
	if !ok || math.Abs(got-0.7) > 1e-9 {
		t.Fatalf("mem_fraction_static = %v, want 0.7", config["mem_fraction_static"])
	}
	if config["max_running_requests"] != float64(16) {
		t.Fatalf("max_running_requests = %v, want 16", config["max_running_requests"])
	}
}

func TestHealerLookupDeploymentRejectsAmbiguousModelName(t *testing.T) {
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			if name != "deploy.list" {
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
			return &ToolResult{Content: `[
				{"name":"qwen3-a","labels":{"aima.dev/model":"qwen3-8b","aima.dev/engine":"vllm","aima.dev/slot":"slot-a"}},
				{"name":"qwen3-b","labels":{"aima.dev/model":"qwen3-8b","aima.dev/engine":"vllm","aima.dev/slot":"slot-b"}}
			]`}, nil
		},
	}

	healer := NewHealer(tools)
	_, err := healer.lookupDeployment(context.Background(), "qwen3-8b")
	if err == nil {
		t.Fatal("expected ambiguity error")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v, want ambiguous", err)
	}
}

func TestTunerStartDerivesDefaultParametersFromResolvedEngine(t *testing.T) {
	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			if name != "knowledge.resolve" {
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
			return &ToolResult{Content: `{"engine":"vllm","config":{"gpu_memory_utilization":0.8}}`}, nil
		},
	}

	tuner := NewTuner(tools)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	session, err := tuner.Start(ctx, TuningConfig{
		Model:      "qwen3-8b",
		MaxConfigs: 2,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	tuner.Stop()

	if len(session.Config.Parameters) != 1 {
		t.Fatalf("parameters = %d, want 1", len(session.Config.Parameters))
	}
	if session.Config.Parameters[0].Key != "gpu_memory_utilization" {
		t.Fatalf("parameter key = %q, want gpu_memory_utilization", session.Config.Parameters[0].Key)
	}
	if session.Total != 2 {
		t.Fatalf("total = %d, want 2", session.Total)
	}
}

func TestTunerRunParsesBenchmarkEnvelopeAndUsesConfigField(t *testing.T) {
	var deployArgs []map[string]any
	var benchmarkArgs []map[string]any

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.run":
				var payload map[string]any
				if err := json.Unmarshal(arguments, &payload); err != nil {
					t.Fatalf("unmarshal deploy args: %v", err)
				}
				deployArgs = append(deployArgs, payload)
				return &ToolResult{Content: `{"status":"ready","address":"127.0.0.1:30000","config":{"gpu_memory_utilization":0.8}}`}, nil
			case "benchmark.run":
				var payload map[string]any
				if err := json.Unmarshal(arguments, &payload); err != nil {
					t.Fatalf("unmarshal benchmark args: %v", err)
				}
				benchmarkArgs = append(benchmarkArgs, payload)
				return &ToolResult{Content: `{"result":{"throughput_tps":42.5,"ttft_p95_ms":123.4},"saved":true}`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	tuner := NewTuner(tools)
	session, err := tuner.Start(context.Background(), TuningConfig{
		Model:      "qwen3-8b",
		Engine:     "vllm",
		MaxConfigs: 1,
		Parameters: []TunableParam{{
			Key:    "gpu_memory_utilization",
			Values: []any{0.8},
		}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current := tuner.CurrentSession()
		if current != nil && current.Status != "running" {
			session = current
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if session.Status != "completed" {
		t.Fatalf("session status = %q, want completed", session.Status)
	}
	if len(session.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(session.Results))
	}
	if session.Results[0].ThroughputTPS != 42.5 {
		t.Fatalf("throughput = %v, want 42.5", session.Results[0].ThroughputTPS)
	}
	if session.Results[0].TTFTP95Ms != 123.4 {
		t.Fatalf("ttft_p95 = %v, want 123.4", session.Results[0].TTFTP95Ms)
	}
	if len(deployArgs) != 2 {
		t.Fatalf("deploy.run calls = %d, want 2", len(deployArgs))
	}
	for _, payload := range deployArgs {
		if _, ok := payload["config"]; !ok {
			t.Fatalf("deploy payload missing config field: %#v", payload)
		}
		if payload["no_pull"] != true {
			t.Fatalf("deploy no_pull = %#v, want true", payload["no_pull"])
		}
		if _, ok := payload["config_overrides"]; ok {
			t.Fatalf("deploy payload still uses config_overrides: %#v", payload)
		}
	}
	if len(benchmarkArgs) != 1 {
		t.Fatalf("benchmark.run calls = %d, want 1", len(benchmarkArgs))
	}
	if benchmarkArgs[0]["endpoint"] != "http://127.0.0.1:30000/v1/chat/completions" {
		t.Fatalf("benchmark endpoint = %#v, want direct deployment endpoint", benchmarkArgs[0]["endpoint"])
	}
	if _, ok := benchmarkArgs[0]["deploy_config"]; !ok {
		t.Fatalf("benchmark args missing deploy_config: %#v", benchmarkArgs[0])
	}
}

func TestExplorationManagerTunePersistsRun(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.run":
				return &ToolResult{Content: `{"status":"ready","address":"127.0.0.1:30000","config":{"gpu_memory_utilization":0.8}}`}, nil
			case "benchmark.run":
				return &ToolResult{Content: `{"result":{"throughput_tps":42.5,"ttft_p95_ms":123.4},"saved":true}`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	tuner := NewTuner(tools)
	manager := NewExplorationManager(db, tuner, tools)
	run, err := manager.Start(ctx, ExplorationStart{
		Kind: "tune",
		Target: ExplorationTarget{
			Hardware: "nvidia-gb10-arm64",
			Model:    "qwen3-8b",
			Engine:   "vllm",
		},
		SearchSpace: map[string][]any{
			"gpu_memory_utilization": {0.8},
		},
		Benchmark: ExplorationBenchmarkProfile{
			Concurrency: 2,
			Rounds:      3,
		},
		Constraints: ExplorationConstraints{
			MaxCandidates: 1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var status *ExplorationStatus
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err = manager.Result(ctx, run.ID)
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if status.Run.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if status == nil || status.Run.Status != "completed" {
		t.Fatalf("run status = %v, want completed", status)
	}
	if status.Run.SummaryJSON == "" {
		t.Fatal("expected summary_json to be populated")
	}
	if len(status.Events) < 2 {
		t.Fatalf("events = %d, want at least 2", len(status.Events))
	}
	var tuningEvent *state.ExplorationEvent
	for _, event := range status.Events {
		if event.ToolName == "tuning" && event.Status == "running" {
			tuningEvent = event
			break
		}
	}
	if tuningEvent == nil {
		t.Fatalf("expected tuning running event, got %#v", status.Events)
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(tuningEvent.RequestJSON), &request); err != nil {
		t.Fatalf("Unmarshal tuning request: %v", err)
	}
	if got := request["concurrency"]; got != float64(2) {
		t.Fatalf("tuning request concurrency = %v, want 2", got)
	}
	if got := request["rounds"]; got != float64(3) {
		t.Fatalf("tuning request rounds = %v, want 3", got)
	}
}

func TestExplorationManagerValidatePersistsRun(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.run":
				return &ToolResult{Content: `{"status":"ready","name":"qwen3-8b-vllm","address":"127.0.0.1:30000","config":{"gpu_memory_utilization":0.8}}`}, nil
			case "benchmark.run":
				var args map[string]any
				if err := json.Unmarshal(arguments, &args); err != nil {
					t.Fatalf("Unmarshal benchmark args: %v", err)
				}
				if args["model"] != "qwen3-8b" {
					t.Fatalf("model = %v, want qwen3-8b", args["model"])
				}
				if args["hardware"] != "nvidia-gb10-arm64" {
					t.Fatalf("hardware = %v, want nvidia-gb10-arm64", args["hardware"])
				}
				if args["engine"] != "vllm" {
					t.Fatalf("engine = %v, want vllm", args["engine"])
				}
				if args["endpoint"] != "http://127.0.0.1:30000/v1/chat/completions" {
					t.Fatalf("endpoint = %v, want direct deployment endpoint", args["endpoint"])
				}
				if _, ok := args["deploy_config"]; !ok {
					t.Fatalf("deploy_config missing from benchmark args: %#v", args)
				}
				return &ToolResult{Content: `{"result":{"throughput_tps":51.2},"saved":true,"benchmark_id":"bench-001","config_id":"cfg-001"}`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	run, err := manager.Start(ctx, ExplorationStart{
		Kind: "validate",
		Target: ExplorationTarget{
			Hardware: "nvidia-gb10-arm64",
			Model:    "qwen3-8b",
			Engine:   "vllm",
		},
		Benchmark: ExplorationBenchmarkProfile{
			Concurrency: 2,
			Rounds:      3,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var status *ExplorationStatus
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err = manager.Result(ctx, run.ID)
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if status.Run.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if status == nil || status.Run.Status != "completed" {
		t.Fatalf("run status = %v, want completed", status)
	}
	if status.Run.SummaryJSON == "" {
		t.Fatal("expected summary_json to be populated")
	}
	if len(status.Events) != 4 {
		t.Fatalf("events = %d, want 4", len(status.Events))
	}
	if status.Events[1].ToolName != "deploy.run" {
		t.Fatalf("tool name = %q, want deploy.run", status.Events[1].ToolName)
	}
	if status.Events[3].ToolName != "benchmark.run" {
		t.Fatalf("tool name = %q, want benchmark.run", status.Events[3].ToolName)
	}
	if status.Events[3].ArtifactID != "bench-001" {
		t.Fatalf("artifact_id = %q, want bench-001", status.Events[3].ArtifactID)
	}
}

func TestExplorationManagerOpenQuestionAutoResolves(t *testing.T) {
	ctx := context.Background()
	db, err := state.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if err := db.UpsertOpenQuestion(ctx, "oq-001", "stack:hami", "What deviceMemoryScaling works best for vLLM on GB10?", "benchmark", "1.0", "untested", ""); err != nil {
		t.Fatalf("UpsertOpenQuestion: %v", err)
	}

	tools := &mockTools{
		execute: func(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
			switch name {
			case "deploy.run":
				return &ToolResult{Content: `{"status":"ready","name":"qwen3-8b-vllm","address":"127.0.0.1:30001","config":{"gpu_memory_utilization":0.82}}`}, nil
			case "benchmark.run":
				return &ToolResult{Content: `{"result":{"throughput_tps":48.9},"saved":true,"benchmark_id":"bench-oq-001","config_id":"cfg-oq-001"}`}, nil
			default:
				return nil, fmt.Errorf("unexpected tool: %s", name)
			}
		},
	}

	manager := NewExplorationManager(db, nil, tools)
	run, err := manager.Start(ctx, ExplorationStart{
		Kind:      "open_question",
		SourceRef: "oq-001",
		Target: ExplorationTarget{
			Hardware: "nvidia-gb10-arm64",
			Model:    "qwen3-8b",
			Engine:   "vllm",
		},
		Benchmark: ExplorationBenchmarkProfile{
			Concurrency: 2,
			Rounds:      1,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	var status *ExplorationStatus
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err = manager.Result(ctx, run.ID)
		if err != nil {
			t.Fatalf("Result: %v", err)
		}
		if status.Run.Status == "completed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if status == nil || status.Run.Status != "completed" {
		t.Fatalf("run status = %v, want completed", status)
	}
	question, err := db.GetOpenQuestion(ctx, "oq-001")
	if err != nil {
		t.Fatalf("GetOpenQuestion: %v", err)
	}
	if question.Status != "tested" {
		t.Fatalf("question status = %q, want tested", question.Status)
	}
	if !strings.Contains(question.ActualResult, `"benchmark_id":"bench-oq-001"`) {
		t.Fatalf("actual_result = %q, want benchmark reference", question.ActualResult)
	}
	if len(status.Events) != 5 {
		t.Fatalf("events = %d, want 5", len(status.Events))
	}
	if status.Events[4].ToolName != "knowledge.open_questions" {
		t.Fatalf("tool name = %q, want knowledge.open_questions", status.Events[4].ToolName)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// mockToolRejectLLM fails with an error when tools are provided, succeeds without tools.
type mockToolRejectLLM struct {
	responses    []*Response
	calls        int
	toolAttempts int // how many times tools were sent
}

func (m *mockToolRejectLLM) ChatCompletion(_ context.Context, _ []Message, tools []ToolDefinition) (*Response, error) {
	if len(tools) > 0 {
		m.toolAttempts++
		return nil, fmt.Errorf(`"auto" tool choice requires --enable-auto-tool-choice`)
	}
	if m.calls >= len(m.responses) {
		return nil, fmt.Errorf("no more mock responses")
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func TestAgent_ContextOnlyMode_ToolCallRejected(t *testing.T) {
	llm := &mockToolRejectLLM{
		responses: []*Response{
			{Content: "I can answer without tools."},
			{Content: "Still in context-only mode."},
		},
	}
	tools := &mockTools{
		tools: []ToolDefinition{
			{Name: "deploy.list", Description: "List deployments"},
		},
	}

	a := NewAgent(llm, tools)

	// First ask: detects tool rejection, falls back to context-only
	result, _, _, err := a.Ask(context.Background(), "", "what is deployed?")
	if err != nil {
		t.Fatalf("expected context-only fallback, got error: %v", err)
	}
	if result != "I can answer without tools." {
		t.Errorf("result = %q, want degraded response", result)
	}
	if a.ToolMode() != "context_only" {
		t.Errorf("ToolMode = %q, want context_only", a.ToolMode())
	}
	if llm.toolAttempts != 1 {
		t.Errorf("toolAttempts = %d, want 1 (probed once)", llm.toolAttempts)
	}

	// Second ask: should NOT attempt tools again (remembered context-only)
	result, _, _, err = a.Ask(context.Background(), "", "hi again")
	if err != nil {
		t.Fatalf("second ask: %v", err)
	}
	if result != "Still in context-only mode." {
		t.Errorf("result = %q, want context-only response", result)
	}
	if llm.toolAttempts != 1 {
		t.Errorf("toolAttempts = %d after 2nd ask, want 1 (should not retry tools)", llm.toolAttempts)
	}
}
