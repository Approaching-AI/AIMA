package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// mockLLM is a test double that returns predefined responses in sequence.
type mockLLM struct {
	responses []*Response
	calls     int
	messages  [][]Message // record of all calls
}

func (m *mockLLM) ChatCompletion(ctx context.Context, messages []Message) (*Response, error) {
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
}

func (m *mockTools) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error) {
	m.calls = append(m.calls, name)
	if r, ok := m.results[name]; ok {
		return r, nil
	}
	return nil, fmt.Errorf("tool not found: %s", name)
}

func (m *mockTools) ListTools() []ToolDefinition {
	return m.tools
}

// mockZeroClaw implements ZeroClawClient.
type mockZeroClaw struct {
	available bool
	response  string
	sessions  map[string]string
}

func (m *mockZeroClaw) Available() bool {
	return m.available
}

func (m *mockZeroClaw) Ask(ctx context.Context, query string) (string, error) {
	return m.response, nil
}

func (m *mockZeroClaw) AskWithSession(ctx context.Context, sessionID, query string) (string, error) {
	if r, ok := m.sessions[sessionID]; ok {
		return r, nil
	}
	return m.response, nil
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
	result, err := agent.Ask(context.Background(), "Hi")
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
	result, err := agent.Ask(context.Background(), "What GPU do I have?")
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
	result, err := agent.Ask(context.Background(), "Deploy the best model")
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
	_, err := agent.Ask(context.Background(), "test")
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
	_, err := agent.Ask(ctx, "test")
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
	result, err := agent.Ask(context.Background(), "do something")
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
	result, err := agent.Ask(context.Background(), "delete everything")
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
	agent.Ask(context.Background(), "test")

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
	// Check that tool names appear in the prompt
	for _, name := range []string{"hardware.detect", "model.list"} {
		if !contains(sysPrompt, name) {
			t.Errorf("system prompt missing tool %q", name)
		}
	}
}

func TestWithMaxTurns(t *testing.T) {
	agent := NewAgent(nil, nil, WithMaxTurns(5))
	if agent.maxTurns != 5 {
		t.Errorf("maxTurns = %d, want 5", agent.maxTurns)
	}
}

func TestDispatcher_ForceLocal(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)

	zc := &mockZeroClaw{available: true, response: "from L3b"}
	d := NewDispatcher(goAgent, zc)

	result, err := d.Ask(context.Background(), "optimize everything", DispatchOption{ForceLocal: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "from L3a" {
		t.Errorf("result = %q, want from L3a", result)
	}
}

func TestDispatcher_ForceDeep(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)

	zc := &mockZeroClaw{available: true, response: "from L3b"}
	d := NewDispatcher(goAgent, zc)

	result, err := d.Ask(context.Background(), "simple query", DispatchOption{ForceDeep: true})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "from L3b" {
		t.Errorf("result = %q, want from L3b", result)
	}
}

func TestDispatcher_ForceDeepUnavailable(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)

	zc := &mockZeroClaw{available: false}
	d := NewDispatcher(goAgent, zc)

	_, err := d.Ask(context.Background(), "test", DispatchOption{ForceDeep: true})
	if err == nil {
		t.Fatal("expected error when forcing deep with unavailable ZeroClaw")
	}
}

func TestDispatcher_SessionRouting(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)

	zc := &mockZeroClaw{
		available: true,
		sessions:  map[string]string{"sess-1": "session response"},
	}
	d := NewDispatcher(goAgent, zc)

	result, err := d.Ask(context.Background(), "continue", DispatchOption{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "session response" {
		t.Errorf("result = %q, want session response", result)
	}
}

func TestDispatcher_ComplexQueryRouting(t *testing.T) {
	tests := []struct {
		name        string
		query       string
		zcAvailable bool
		wantL3b     bool
	}{
		{"optimize with zc", "optimize my deployment", true, true},
		{"analyze with zc", "analyze the performance", true, true},
		{"why with zc", "why is it slow?", true, true},
		{"plan with zc", "plan the migration", true, true},
		{"all with zc", "list all issues", true, true},
		{"trend with zc", "show me the trend", true, true},
		{"simple with zc", "what GPU do I have?", true, false},
		{"complex without zc", "optimize everything", false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			llm := &mockLLM{
				responses: []*Response{{Content: "from L3a"}},
			}
			tools := &mockTools{tools: []ToolDefinition{}}
			goAgent := NewAgent(llm, tools)

			zc := &mockZeroClaw{available: tt.zcAvailable, response: "from L3b"}
			d := NewDispatcher(goAgent, zc)

			result, err := d.Ask(context.Background(), tt.query, DispatchOption{})
			if err != nil {
				t.Fatalf("Ask: %v", err)
			}

			if tt.wantL3b && result != "from L3b" {
				t.Errorf("expected L3b, got %q", result)
			}
			if !tt.wantL3b && result != "from L3a" {
				t.Errorf("expected L3a, got %q", result)
			}
		})
	}
}

func TestDispatcher_NilZeroClaw(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)

	d := NewDispatcher(goAgent, nil)

	// Should fall back to L3a even with complex query
	result, err := d.Ask(context.Background(), "optimize everything", DispatchOption{})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if result != "from L3a" {
		t.Errorf("result = %q, want from L3a", result)
	}
}

func TestDispatcher_NilZeroClaw_ForceDeep(t *testing.T) {
	llm := &mockLLM{
		responses: []*Response{{Content: "from L3a"}},
	}
	tools := &mockTools{tools: []ToolDefinition{}}
	goAgent := NewAgent(llm, tools)

	d := NewDispatcher(goAgent, nil)

	_, err := d.Ask(context.Background(), "test", DispatchOption{ForceDeep: true})
	if err == nil {
		t.Fatal("expected error when forcing deep with nil ZeroClaw")
	}
}

func TestIsComplexQuery(t *testing.T) {
	tests := []struct {
		query   string
		complex bool
	}{
		{"optimize GPU usage", true},
		{"why is inference slow", true},
		{"analyze performance", true},
		{"plan deployment strategy", true},
		{"show all models", true},
		{"show trend over time", true},
		{"list models", false},
		{"what GPU do I have", false},
		{"deploy qwen3-8b", false},
		{"OPTIMIZE this", true}, // case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := isComplexQuery(tt.query)
			if got != tt.complex {
				t.Errorf("isComplexQuery(%q) = %v, want %v", tt.query, got, tt.complex)
			}
		})
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
