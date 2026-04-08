package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// LLMClient sends messages to an LLM and returns the response.
type LLMClient interface {
	ChatCompletion(ctx context.Context, messages []Message, tools []ToolDefinition) (*Response, error)
}

// Message represents a chat message in the conversation.
type Message struct {
	Role             string     `json:"role"` // system, user, assistant, tool
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"` // preserved for providers that use thinking (e.g. Kimi)
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
}

// ToolCall represents a tool invocation requested by the LLM.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// Response is what the LLM returns.
type Response struct {
	Content          string     `json:"content,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
}

// ToolExecutor executes MCP tools (provided by mcp.Server).
type ToolExecutor interface {
	ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*ToolResult, error)
	ListTools() []ToolDefinition
}

// ProfiledToolExecutor extends ToolExecutor with profile-based tool filtering.
type ProfiledToolExecutor interface {
	ToolExecutor
	ListToolsForProfile(profile string) []ToolDefinition
}

// ToolResult is the outcome of a tool execution.
type ToolResult struct {
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// ToolCallInfo records a single tool call for UI display.
type ToolCallInfo struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Result    string          `json:"result,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ToolDefinition is a serializable tool description.
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// StreamEvent is emitted during AskStream to report tool call progress.
type StreamEvent struct {
	Type      string          `json:"type"`                // "tool_start" or "tool_done"
	Name      string          `json:"name"`                // tool name
	Arguments json.RawMessage `json:"arguments,omitempty"` // tool arguments (tool_start only)
	Result    string          `json:"result,omitempty"`    // tool result (tool_done only)
	IsError   bool            `json:"is_error,omitempty"`  // true if tool errored (tool_done only)
	Index     int             `json:"index"`               // tool call index
}

// StreamCallback is called for each tool call event during AskStream.
type StreamCallback func(StreamEvent)

const defaultMaxTurns = 30

func toolResultError(result *ToolResult) error {
	if result == nil {
		return fmt.Errorf("empty tool result")
	}
	if result.IsError {
		return fmt.Errorf("%s", result.Content)
	}
	if strings.Contains(result.Content, "NEEDS_APPROVAL") {
		return fmt.Errorf("tool requires approval: %s", result.Content)
	}
	return nil
}

// toolMode tracks whether the LLM backend supports tool calling.
type toolMode int

const (
	toolModeUnknown     toolMode = iota // not yet probed
	toolModeEnabled                     // tools work — full agent mode
	toolModeContextOnly                 // tools rejected — context-only chat
)

const toolModeRetryInterval = 5 * time.Minute

// Agent is the L3a Go Agent (simple tool-calling loop).
type Agent struct {
	llm      LLMClient
	tools    ToolExecutor
	maxTurns int
	sessions *SessionStore
	profile  string

	mu             sync.RWMutex
	mode           toolMode
	modeDetectedAt time.Time
}

// AgentOption configures the Agent.
type AgentOption func(*Agent)

// WithMaxTurns sets the maximum number of tool-calling turns.
func WithMaxTurns(n int) AgentOption {
	return func(a *Agent) {
		a.maxTurns = n
	}
}

// WithSessions enables multi-turn session memory.
func WithSessions(s *SessionStore) AgentOption {
	return func(a *Agent) {
		a.sessions = s
	}
}

// WithProfile sets the tool profile for the agent, limiting which tools are visible to the LLM.
func WithProfile(p string) AgentOption {
	return func(a *Agent) {
		a.profile = p
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

// ToolMode returns "enabled", "context_only", or "unknown".
func (a *Agent) ToolMode() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	switch a.mode {
	case toolModeEnabled:
		return "enabled"
	case toolModeContextOnly:
		return "context_only"
	default:
		return "unknown"
	}
}

// toolsActive returns true if tools should be sent in the current request.
// In contextOnly mode, periodically retries tools in case the backend was
// restarted with tool support.
func (a *Agent) toolsActive() bool {
	a.mu.RLock()
	mode := a.mode
	detected := a.modeDetectedAt
	a.mu.RUnlock()

	switch mode {
	case toolModeEnabled:
		return true
	case toolModeContextOnly:
		return time.Since(detected) > toolModeRetryInterval
	default: // unknown
		return true
	}
}

func (a *Agent) setToolMode(m toolMode) {
	a.mu.Lock()
	a.mode = m
	a.modeDetectedAt = time.Now()
	a.mu.Unlock()
}

// isToolRejectionError checks if the error is caused by the backend not
// supporting tool calling (e.g., vLLM missing --enable-auto-tool-choice).
func isToolRejectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "enable-auto-tool-choice") ||
		strings.Contains(msg, "tools is not supported")
}

// Ask processes a user query through the agent loop and returns the final text response.
// If sessionID is empty, a new session is created. Returns (result, sessionID, toolCalls, error).
func (a *Agent) Ask(ctx context.Context, sessionID, query string) (string, string, []ToolCallInfo, error) {
	return a.AskStream(ctx, sessionID, query, nil)
}

// AskStream is like Ask but calls cb for each tool call start/finish event.
func (a *Agent) AskStream(ctx context.Context, sessionID, query string, cb StreamCallback) (string, string, []ToolCallInfo, error) {
	if a.llm == nil {
		return "", "", nil, fmt.Errorf("no LLM backend configured: deploy a model and run 'aima serve', or set AIMA_LLM_ENDPOINT")
	}

	// Session management: load or create
	if sessionID == "" {
		sessionID = GenerateID()
	}
	var messages []Message
	if a.sessions != nil {
		if prev, ok := a.sessions.Get(sessionID); ok {
			messages = prev
		}
	}
	if len(messages) == 0 {
		messages = []Message{{Role: "system", Content: a.buildSystemPrompt()}}
	}
	messages = append(messages, Message{Role: "user", Content: query})

	var allTools []ToolDefinition
	if a.profile != "" {
		if pt, ok := a.tools.(ProfiledToolExecutor); ok {
			allTools = pt.ListToolsForProfile(a.profile)
		} else {
			allTools = a.tools.ListTools()
		}
	} else {
		allTools = a.tools.ListTools()
	}
	useTools := len(allTools) > 0 && a.toolsActive()
	var allToolCalls []ToolCallInfo

	for turn := 0; turn < a.maxTurns; turn++ {
		select {
		case <-ctx.Done():
			return "", "", allToolCalls, ctx.Err()
		default:
		}

		var activeTools []ToolDefinition
		if useTools {
			activeTools = allTools
		}
		resp, err := a.llm.ChatCompletion(ctx, messages, activeTools)
		if err != nil && useTools && isToolRejectionError(err) {
			// Backend doesn't support tools → switch to context-only mode.
			slog.Warn("tool calling not supported, switching to context-only mode", "error", err)
			a.setToolMode(toolModeContextOnly)
			useTools = false
			resp, err = a.llm.ChatCompletion(ctx, messages, nil)
		}
		if err != nil {
			return "", "", allToolCalls, fmt.Errorf("chat completion (turn %d): %w", turn, err)
		}
		if useTools && a.mode == toolModeUnknown {
			a.setToolMode(toolModeEnabled)
		}

		// If no tool calls, return the text response
		if len(resp.ToolCalls) == 0 {
			messages = append(messages, Message{Role: "assistant", Content: resp.Content, ReasoningContent: resp.ReasoningContent})
			if a.sessions != nil {
				a.sessions.Save(sessionID, messages)
			}
			return resp.Content, sessionID, allToolCalls, nil
		}

		// Append assistant message with tool calls
		messages = append(messages, Message{
			Role:             "assistant",
			Content:          resp.Content,
			ReasoningContent: resp.ReasoningContent,
			ToolCalls:        resp.ToolCalls,
		})

		// Execute each tool call and append results
		for _, tc := range resp.ToolCalls {
			slog.Debug("executing tool", "name", tc.Name, "id", tc.ID)

			callInfo := ToolCallInfo{
				Name:      tc.Name,
				Arguments: json.RawMessage(tc.Arguments),
			}
			idx := len(allToolCalls)

			if cb != nil {
				cb(StreamEvent{Type: "tool_start", Name: tc.Name, Arguments: json.RawMessage(tc.Arguments), Index: idx})
			}

			result, err := a.tools.ExecuteTool(ctx, tc.Name, json.RawMessage(tc.Arguments))
			if err != nil {
				callInfo.Result = err.Error()
				callInfo.IsError = true
				messages = append(messages, Message{
					Role:       "tool",
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("error: %v", err),
				})
				allToolCalls = append(allToolCalls, callInfo)
				if cb != nil {
					cb(StreamEvent{Type: "tool_done", Name: tc.Name, Result: callInfo.Result, IsError: true, Index: idx})
				}
				continue
			}

			content := result.Content
			if result.IsError {
				callInfo.IsError = true
				content = "error: " + content
			}
			callInfo.Result = content
			messages = append(messages, Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    content,
			})
			allToolCalls = append(allToolCalls, callInfo)
			if cb != nil {
				cb(StreamEvent{Type: "tool_done", Name: tc.Name, Result: content, IsError: result.IsError, Index: idx})
			}
		}
	}

	return "", "", allToolCalls, fmt.Errorf("agent exceeded maximum turns (%d)", a.maxTurns)
}

func (a *Agent) buildSystemPrompt() string {
	return corePrompt
}
