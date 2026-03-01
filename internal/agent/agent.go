package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// LLMClient sends messages to an LLM and returns the response.
type LLMClient interface {
	ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error)
}

// Message represents a chat message in the conversation.
type Message struct {
	Role       string     `json:"role"` // system, user, assistant, tool
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Response is what the LLM returns.
type Response struct {
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolExecutor executes MCP tools (provided by mcp.Server).
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error)
	ListTools() []ToolDefinition
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// ToolDefinition is a serializable tool description.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

const defaultMaxTurns = 30

// Agent is the L3a Go Agent (simple tool-calling loop).
type Agent struct {
	llm      LLMClient
	tools    ToolExecutor
	maxTurns int
}

// AgentOption configures the Agent.
type AgentOption func(*Agent)

// WithMaxTurns sets the maximum number of tool-calling turns.
func WithMaxTurns(n int) AgentOption {
	return func(a *Agent) {
		a.maxTurns = n
	}
}

// NewAgent creates a new L3a agent.
func NewAgent(llm LLMClient, tools ToolExecutor, opts ...AgentOption) *Agent {
	a := &Agent{
		llm:      llm,
		tools:    tools,
		maxTurns: defaultMaxTurns,
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Available reports whether the agent has an LLM client configured.
func (a *Agent) Available() bool {
	return a.llm != nil
}

// Ask processes a user query through the agent loop and returns the final text response.
func (a *Agent) Ask(ctx context.Context, query string) (string, error) {
	if a.llm == nil {
		return "", fmt.Errorf("no LLM backend configured: deploy a model and run 'aima serve', or set AIMA_LLM_ENDPOINT")
	}

	tools := a.tools.ListTools()
	messages := []Message{
		{Role: "system", Content: a.buildSystemPrompt()},
		{Role: "user", Content: query},
	}

	for turn := 0; turn < a.maxTurns; turn++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		resp, err := a.llm.ChatCompletion(ctx, messages, tools)
		if err != nil {
			return "", fmt.Errorf("chat completion (turn %d): %w", turn, err)
		}

		// If no tool calls, return the text response
		if len(resp.ToolCalls) == 0 {
			return resp.Content, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call and append results
		for _, tc := range resp.ToolCalls {
			slog.Debug("executing tool", "name", tc.Name, "id", tc.ID)

			result, err := a.tools.ExecuteTool(ctx, tc.Name, json.RawMessage(tc.Arguments))
			if err != nil {
				messages = append(messages, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("error: %v", err),
				})
				continue
			}

			content := result.Content
			if result.IsError {
				content = "error: " + content
			}
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
		}
	}

	return "", fmt.Errorf("agent exceeded maximum turns (%d)", a.maxTurns)
}

func (a *Agent) buildSystemPrompt() string {
	return "You are AIMA, an AI inference management assistant. " +
		"You have access to tools that let you detect hardware, manage models and engines, " +
		"deploy inference services, and query the knowledge base. " +
		"Use these tools to help the user manage AI inference on their device."
}
