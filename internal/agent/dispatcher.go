package agent

import (
	"context"
	"fmt"
	"strings"
)

// ZeroClawClient is the interface for the ZeroClaw sidecar (L3b).
type ZeroClawClient interface {
	Available() bool
	SupportsSessions() bool
	Ask(ctx context.Context, query string) (string, error)
	AskWithSession(ctx context.Context, sessionID, query string) (string, error)
}

// DispatchOption controls routing behavior.
type DispatchOption struct {
	ForceLocal     bool           // --local flag: force L3a
	ForceDeep      bool           // --deep flag: force L3b
	SessionID      string         // --session flag: continue L3a or L3b session
	StreamCallback StreamCallback // optional: stream tool call events
}

// Dispatcher routes queries to L3a (Go Agent) or L3b (ZeroClaw).
type Dispatcher struct {
	goAgent  *Agent
	zeroclaw ZeroClawClient
}

// NewDispatcher creates a new dispatcher. zeroclaw may be nil if not available.
func NewDispatcher(goAgent *Agent, zeroclaw ZeroClawClient) *Dispatcher {
	return &Dispatcher{
		goAgent:  goAgent,
		zeroclaw: zeroclaw,
	}
}

// Ask routes the query to the appropriate agent based on options and heuristics.
// Returns (result, sessionID, toolCalls, error). sessionID is always returned for L3a sessions.
func (d *Dispatcher) Ask(ctx context.Context, query string, opts DispatchOption) (string, string, []ToolCallInfo, error) {
	// Force local → L3a
	if opts.ForceLocal {
		return d.goAgent.AskStream(ctx, opts.SessionID, query, opts.StreamCallback)
	}

	// Force deep → L3b (no session fallback)
	if opts.ForceDeep {
		if !d.zeroClawAvailable() {
			return "", "", nil, fmt.Errorf("ZeroClaw (L3b) is not available")
		}
		if opts.SessionID != "" {
			if !d.zeroClawSupportsSessions() {
				return "", "", nil, fmt.Errorf("ZeroClaw (L3b) does not support named sessions in daemon mode; retry without --session or use --local")
			}
			r, err := d.zeroclaw.AskWithSession(ctx, opts.SessionID, query)
			return r, opts.SessionID, nil, err
		}
		r, err := d.zeroclaw.Ask(ctx, query)
		return r, "", nil, err
	}

	// Session ID without force-deep → try L3b, fall back to L3a
	if opts.SessionID != "" {
		if d.zeroClawAvailable() && d.zeroClawSupportsSessions() {
			r, err := d.zeroclaw.AskWithSession(ctx, opts.SessionID, query)
			return r, opts.SessionID, nil, err
		}
		// Graceful degradation: L3b unavailable → use L3a session
		return d.goAgent.AskStream(ctx, opts.SessionID, query, opts.StreamCallback)
	}

	// Auto-route: if ZeroClaw available and query looks complex, use L3b
	if d.zeroClawAvailable() && isComplexQuery(query) {
		r, err := d.zeroclaw.Ask(ctx, query)
		return r, "", nil, err
	}

	// Default: L3a
	return d.goAgent.AskStream(ctx, "", query, opts.StreamCallback)
}

func (d *Dispatcher) zeroClawAvailable() bool {
	return d.zeroclaw != nil && d.zeroclaw.Available()
}

func (d *Dispatcher) zeroClawSupportsSessions() bool {
	return d.zeroclaw != nil && d.zeroclaw.SupportsSessions()
}

// complexKeywords triggers routing to L3b when present in the query.
// Fixed-size array to prevent accidental mutation.
var complexKeywords = [...]string{
	"optimize",
	"why",
	"analyze",
	"plan",
	"all",
	"trend",
}

func isComplexQuery(query string) bool {
	lower := strings.ToLower(query)
	for _, kw := range complexKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
