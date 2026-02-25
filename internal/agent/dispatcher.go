package agent

import (
	"context"
	"fmt"
	"strings"
)

// ZeroClawClient is the interface for the ZeroClaw sidecar (L3b).
type ZeroClawClient interface {
	Available() bool
	Ask(ctx context.Context, query string) (string, error)
	AskWithSession(ctx context.Context, sessionID, query string) (string, error)
}

// DispatchOption controls routing behavior.
type DispatchOption struct {
	ForceLocal bool   // --local flag: force L3a
	ForceDeep  bool   // --deep flag: force L3b
	SessionID  string // --session flag: continue ZeroClaw session
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
func (d *Dispatcher) Ask(ctx context.Context, query string, opts DispatchOption) (string, error) {
	// Force local → L3a
	if opts.ForceLocal {
		return d.goAgent.Ask(ctx, query)
	}

	// Force deep or session → L3b
	if opts.ForceDeep || opts.SessionID != "" {
		if !d.zeroClawAvailable() {
			return "", fmt.Errorf("ZeroClaw (L3b) is not available")
		}
		if opts.SessionID != "" {
			return d.zeroclaw.AskWithSession(ctx, opts.SessionID, query)
		}
		return d.zeroclaw.Ask(ctx, query)
	}

	// Auto-route: if ZeroClaw available and query looks complex, use L3b
	if d.zeroClawAvailable() && isComplexQuery(query) {
		return d.zeroclaw.Ask(ctx, query)
	}

	// Default: L3a
	return d.goAgent.Ask(ctx, query)
}

func (d *Dispatcher) zeroClawAvailable() bool {
	return d.zeroclaw != nil && d.zeroclaw.Available()
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
