package agent

import "context"

// DispatchOption controls routing behavior.
type DispatchOption struct {
	SessionID      string         // --session flag: continue a session
	StreamCallback StreamCallback // optional: stream tool call events
}

// Dispatcher routes queries to the Go Agent (L3a).
type Dispatcher struct {
	goAgent *Agent
}

// NewDispatcher creates a new dispatcher.
func NewDispatcher(goAgent *Agent) *Dispatcher {
	return &Dispatcher{goAgent: goAgent}
}

// Ask routes the query to the Go Agent.
// Returns (result, sessionID, toolCalls, error).
func (d *Dispatcher) Ask(ctx context.Context, query string, opts DispatchOption) (string, string, []ToolCallInfo, error) {
	return d.goAgent.AskStream(ctx, opts.SessionID, query, opts.StreamCallback)
}
