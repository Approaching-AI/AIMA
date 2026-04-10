package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"sync"
	"time"

	"github.com/jguan/aima/internal/agent"
	"github.com/jguan/aima/internal/engine"
	"github.com/jguan/aima/internal/fleet"
	"github.com/jguan/aima/internal/k3s"
	"github.com/jguan/aima/internal/knowledge"
	"github.com/jguan/aima/internal/mcp"
	"github.com/jguan/aima/internal/openclaw"
	"github.com/jguan/aima/internal/proxy"
	"github.com/jguan/aima/internal/stack"

	state "github.com/jguan/aima/internal"
)

// fleetBlockedTools lists MCP tools that fleet.exec must not daisy-chain remotely.
// This is transport policy only; agent-specific policy stays in isBlockedAgentTool.
var fleetBlockedTools = map[string]string{
	"fleet.exec": "recursive fleet execution blocked",
}

// confirmableTools lists MCP tools that require user confirmation when called by the Agent.
// These are NOT blocked: instead, the adapter runs a dry-run and returns NEEDS_APPROVAL.
// The user can then approve via deploy.approve, or re-run with --dangerously-skip-permissions.
var confirmableTools = map[string]string{
	"deploy.apply": "creates or replaces inference deployment",
}

// blockedAgentTools lists MCP tools that the Agent must not call directly.
// These are blocked at the adapter level; users can still invoke them via CLI.
var blockedAgentTools = map[string]string{
	"model.remove":   "destructive operation",
	"engine.remove":  "destructive operation",
	"deploy.delete":  "destructive operation",
	"shell.exec":     "arbitrary command execution",
	"agent.ask":      "recursive agent invocation",
	"agent.rollback": "state rollback mutation",
}

func isBlockedAgentTool(name string, arguments json.RawMessage) (bool, string) {
	if reason, ok := blockedAgentTools[name]; ok {
		return true, reason
	}

	// system.config supports both get and set. Agent may read, but writes are blocked.
	// Block when "value" key is present in the JSON (regardless of its value, including null).
	if name == "system.config" && len(arguments) > 0 {
		var raw map[string]json.RawMessage
		if json.Unmarshal(arguments, &raw) == nil {
			if _, hasValue := raw["value"]; hasValue {
				return true, "persistent configuration mutation"
			}
		}
	}

	// stack is a merged action tool: only init is destructive.
	if name == "stack" {
		if action, ok := jsonFieldString(arguments, "action"); ok && action == "init" {
			return true, "stack init is infrastructure mutation"
		}
	}

	// explore is a merged action tool: only start is blocked for the Agent.
	if name == "explore" {
		if action, ok := jsonFieldString(arguments, "action"); ok && action == "start" {
			return true, "explore start is blocked for Agent-initiated calls"
		}
	}

	// fleet.exec unwraps the inner tool_name and applies the same guardrails
	// to the remote target before the adapter decides whether approval is needed.
	if name == "fleet.exec" {
		innerTool, innerParams, ok := fleetExecTarget(arguments)
		if !ok {
			return true, "fleet.exec: cannot determine inner tool_name"
		}
		if blocked, reason := isBlockedFleetExecTarget(innerTool); blocked {
			return true, reason
		}
		return isBlockedAgentTool(innerTool, innerParams)
	}

	// catalog.override: allow engine_asset and model_asset (inference tuning),
	// block hardware_profile, partition_strategy, stack_component (infrastructure safety).
	if name == "catalog.override" {
		if len(arguments) > 0 {
			var raw map[string]json.RawMessage
			if json.Unmarshal(arguments, &raw) == nil {
				if kindRaw, ok := raw["kind"]; ok {
					var kind string
					if json.Unmarshal(kindRaw, &kind) == nil {
						switch kind {
						case "engine_asset", "model_asset":
							return false, ""
						}
					}
				}
			}
		}
		return true, "catalog override restricted to engine/model assets for Agent"
	}

	return false, ""
}

func jsonFieldString(arguments json.RawMessage, key string) (string, bool) {
	if len(arguments) == 0 {
		return "", false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &raw); err != nil {
		return "", false
	}
	value, ok := raw[key]
	if !ok {
		return "", false
	}
	var out string
	if err := json.Unmarshal(value, &out); err != nil {
		return "", false
	}
	return out, true
}

func fleetExecTarget(arguments json.RawMessage) (string, json.RawMessage, bool) {
	if len(arguments) == 0 {
		return "", nil, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &raw); err != nil {
		return "", nil, false
	}
	toolNameRaw, ok := raw["tool_name"]
	if !ok {
		return "", nil, false
	}
	var innerTool string
	if err := json.Unmarshal(toolNameRaw, &innerTool); err != nil || innerTool == "" {
		return "", nil, false
	}
	innerParams, ok := raw["params"]
	if !ok {
		innerParams = nil
	}
	return innerTool, innerParams, true
}

func isBlockedFleetExecTarget(name string) (bool, string) {
	reason, ok := fleetBlockedTools[name]
	return ok, reason
}

type ctxKey string

const ctxKeySkipPerms ctxKey = "skipPerms"

// mcpToolAdapter bridges mcp.Server to agent.ToolExecutor interface.
// It also enforces agent safety guardrails: destructive-op blocking, confirmation gates, and audit logging.
type mcpToolAdapter struct {
	server *mcp.Server
	db     *state.DB

	mu      sync.Mutex
	pending map[int64]*pendingApproval
	nextID  int64
}

type pendingApproval struct {
	toolName  string
	arguments json.RawMessage
	createdAt time.Time
}

func (a *mcpToolAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*agent.ToolResult, error) {
	skipPerms, _ := ctx.Value(ctxKeySkipPerms).(bool)

	// Gap 1: Block high-risk operations from the Agent (unless internal automation).
	if !skipPerms {
		if blocked, reason := isBlockedAgentTool(name, arguments); blocked {
			msg := fmt.Sprintf("BLOCKED: %s is blocked for Agent-initiated calls (%s). Ask the user to run it via CLI instead.", name, reason)
			a.audit(ctx, name, string(arguments), "BLOCKED")
			return &agent.ToolResult{Content: msg, IsError: true}, nil
		}
	}

	// fleet.exec wrapping a confirmable inner tool → needs approval too.
	if name == "fleet.exec" && !skipPerms {
		if len(arguments) > 0 {
			var raw map[string]json.RawMessage
			if json.Unmarshal(arguments, &raw) == nil {
				if tnRaw, ok := raw["tool_name"]; ok {
					var innerTool string
					if json.Unmarshal(tnRaw, &innerTool) == nil {
						if reason, ok := confirmableTools[innerTool]; ok {
							// Run remote dry-run via fleet.exec itself.
							dryArgs, _ := json.Marshal(map[string]any{
								"device_id": json.RawMessage(raw["device_id"]),
								"tool_name": "deploy.dry_run",
								"params":    json.RawMessage(raw["params"]),
							})
							dryResult, drErr := a.server.ExecuteTool(ctx, "fleet.exec", dryArgs)
							var planText string
							if drErr == nil {
								for _, c := range dryResult.Content {
									planText += c.Text
								}
							} else {
								planText = "remote dry-run unavailable: " + drErr.Error()
							}
							id := a.storePending(name, arguments)
							msg := fmt.Sprintf("NEEDS_APPROVAL\n"+
								"Approval ID: %d\n"+
								"Tool: %s\n"+
								"Reason: %s\n\n"+
								"Deployment plan:\n%s\n\n"+
								"Present this plan to the user. When the user approves, call deploy.approve with id=%d.",
								id, innerTool, reason, planText, id)
							a.audit(ctx, name, string(arguments), fmt.Sprintf("NEEDS_APPROVAL id=%d", id))
							return &agent.ToolResult{Content: msg, IsError: false}, nil
						}
					}
				}
			}
		}
	}

	if reason, ok := confirmableTools[name]; ok && !skipPerms {
		dryResult, drErr := a.server.ExecuteTool(ctx, "deploy.dry_run", arguments)
		var planText string
		if drErr == nil {
			for _, c := range dryResult.Content {
				planText += c.Text
			}
		} else {
			planText = "dry-run unavailable: " + drErr.Error()
		}

		id := a.storePending(name, arguments)

		msg := fmt.Sprintf("NEEDS_APPROVAL\n"+
			"Approval ID: %d\n"+
			"Tool: %s\n"+
			"Reason: %s\n\n"+
			"Deployment plan:\n%s\n\n"+
			"Present this plan to the user. When the user approves, call deploy.approve with id=%d.",
			id, name, reason, planText, id)
		a.audit(ctx, name, string(arguments), fmt.Sprintf("NEEDS_APPROVAL id=%d", id))
		return &agent.ToolResult{Content: msg, IsError: false}, nil
	}

	result, err := a.server.ExecuteTool(ctx, name, arguments)
	if err != nil {
		a.audit(ctx, name, string(arguments), "ERROR: "+err.Error())
		return nil, err
	}
	// Convert mcp.ToolResult to agent.ToolResult
	var text string
	for _, c := range result.Content {
		text += c.Text
	}
	// Gap 2: Audit log every agent tool call
	summary := text
	if result.IsError {
		summary = "ERROR: " + text
	}
	a.audit(ctx, name, string(arguments), truncateStr(summary, 500))
	return &agent.ToolResult{
		Content: text,
		IsError: result.IsError,
	}, nil
}

// audit writes to audit_log. Failures are logged but do not block the tool call.
func (a *mcpToolAdapter) audit(ctx context.Context, tool, args, result string) {
	if a.db == nil {
		return
	}
	if err := a.db.LogAction(ctx, &state.AuditEntry{
		AgentType:     "L3a",
		ToolName:      tool,
		Arguments:     truncateStr(args, 500),
		ResultSummary: result,
	}); err != nil {
		slog.Warn("audit log write failed", "tool", tool, "error", err)
	}
}

// truncateStr truncates s to maxLen bytes, appending "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// storePending saves a pending approval and returns its ID. Expired entries (>30min) are pruned.
func (a *mcpToolAdapter) storePending(tool string, args json.RawMessage) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for id, p := range a.pending {
		if now.Sub(p.createdAt) > 30*time.Minute {
			delete(a.pending, id)
		}
	}
	a.nextID++
	a.pending[a.nextID] = &pendingApproval{
		toolName:  tool,
		arguments: append(json.RawMessage{}, args...), // copy
		createdAt: now,
	}
	return a.nextID
}

// executeApproval looks up a pending approval by ID, executes it on the MCP server
// (bypassing the adapter's confirmation gate), and removes the entry.
// Safety: blocked tools can never reach the pending map (blocked check runs first in ExecuteTool).
func (a *mcpToolAdapter) executeApproval(ctx context.Context, id int64) (json.RawMessage, error) {
	a.mu.Lock()
	p, ok := a.pending[id]
	if ok {
		delete(a.pending, id)
	}
	a.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("approval %d not found or expired", id)
	}

	// Defense-in-depth: re-check blocked tools (should never happen since blocked check
	// runs before confirmable check in ExecuteTool, but guard against future changes).
	if blocked, reason := isBlockedAgentTool(p.toolName, p.arguments); blocked {
		a.audit(ctx, "deploy.approve", fmt.Sprintf("id=%d", id), "BLOCKED: "+reason)
		return nil, fmt.Errorf("approval %d references blocked tool %s: %s", id, p.toolName, reason)
	}

	a.audit(ctx, p.toolName, string(p.arguments), fmt.Sprintf("APPROVED via deploy.approve id=%d", id))
	result, err := a.server.ExecuteTool(ctx, p.toolName, p.arguments)
	if err != nil {
		a.audit(ctx, p.toolName, string(p.arguments), "ERROR: "+err.Error())
		return nil, err
	}
	var text string
	for _, c := range result.Content {
		text += c.Text
	}
	a.audit(ctx, p.toolName, string(p.arguments), truncateStr(text, 500))
	return json.RawMessage(text), nil
}

func (a *mcpToolAdapter) ListTools() []agent.ToolDefinition {
	mcpDefs := a.server.ListTools()
	defs := make([]agent.ToolDefinition, len(mcpDefs))
	for i, d := range mcpDefs {
		defs[i] = agent.ToolDefinition{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.InputSchema,
		}
	}
	return defs
}

func (a *mcpToolAdapter) ListToolsForProfile(profile string) []agent.ToolDefinition {
	mcpDefs := a.server.ListToolsForProfile(mcp.Profile(profile))
	defs := make([]agent.ToolDefinition, len(mcpDefs))
	for i, d := range mcpDefs {
		defs[i] = agent.ToolDefinition{
			Name:        d.Name,
			Description: d.Description,
			InputSchema: d.InputSchema,
		}
	}
	return defs
}

type automationToolAdapter struct {
	base *mcpToolAdapter
}

func (a *automationToolAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (*agent.ToolResult, error) {
	ctx = context.WithValue(ctx, ctxKeySkipPerms, true)
	return a.base.ExecuteTool(ctx, name, arguments)
}

func (a *automationToolAdapter) ListTools() []agent.ToolDefinition {
	return a.base.ListTools()
}

func (a *automationToolAdapter) ListToolsForProfile(profile string) []agent.ToolDefinition {
	return a.base.ListToolsForProfile(profile)
}

// fleetMCPAdapter bridges mcp.Server to fleet.MCPExecutor interface.
type fleetMCPAdapter struct {
	server *mcp.Server
}

func (a *fleetMCPAdapter) ExecuteTool(ctx context.Context, name string, arguments json.RawMessage) (json.RawMessage, error) {
	result, err := a.server.ExecuteTool(ctx, name, arguments)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func (a *fleetMCPAdapter) ListToolDefs() json.RawMessage {
	data, _ := json.Marshal(a.server.ListTools())
	return data
}

// toEngineBinarySource converts a knowledge.EngineSource to engine.BinarySource.
// Centralises the mapping so callers don't repeat the 4-field struct literal.
func toEngineBinarySource(src *knowledge.EngineSource) *engine.BinarySource {
	var probePaths []string
	if src != nil && src.Probe != nil {
		probePaths = append(probePaths, src.Probe.Paths...)
	}
	return &engine.BinarySource{
		Binary:      src.Binary,
		Platforms:   src.Platforms,
		Download:    src.Download,
		Mirror:      src.Mirror,
		SHA256:      src.SHA256,
		InstallType: src.InstallType,
		ProbePaths:  probePaths,
	}
}

// execRunner implements engine.CommandRunner using real exec.
type execRunner struct{}

func (r *execRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (r *execRunner) Pipe(ctx context.Context, from, to []string) error {
	fromCmd := exec.CommandContext(ctx, from[0], from[1:]...)
	toCmd := exec.CommandContext(ctx, to[0], to[1:]...)

	pipe, err := fromCmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("create pipe: %w", err)
	}
	toCmd.Stdin = pipe

	if err := fromCmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", from[0], err)
	}
	if err := toCmd.Start(); err != nil {
		_ = fromCmd.Process.Kill()
		_ = fromCmd.Wait()
		return fmt.Errorf("%s: %w", to[0], err)
	}

	// Wait for both concurrently. If the receiver (toCmd) dies early
	// (e.g., permission denied on containerd socket), kill the sender
	// to avoid blocking on a dead pipe.
	toErr := make(chan error, 1)
	go func() { toErr <- toCmd.Wait() }()

	fromErr := fromCmd.Wait()
	tErr := <-toErr

	if tErr != nil {
		_ = fromCmd.Process.Kill()
		return fmt.Errorf("%s: %w", to[0], tErr)
	}
	if fromErr != nil {
		return fmt.Errorf("%s: %w", from[0], fromErr)
	}
	return nil
}

func (r *execRunner) RunStream(ctx context.Context, onLine func(line string), name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe for %s: %w", name, err)
	}
	cmd.Stderr = cmd.Stdout // merge stderr into stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}
	scanner := bufio.NewScanner(stdout)
	// Docker pull JSON lines can be long; increase buffer
	scanner.Buffer(make([]byte, 0, 64*1024), 256*1024)
	for scanner.Scan() {
		onLine(scanner.Text())
	}
	return cmd.Wait()
}

// podQuerierAdapter bridges k3s.Client to stack.PodQuerier interface.
type podQuerierAdapter struct {
	client *k3s.Client
}

func (a *podQuerierAdapter) ListPodsByLabel(ctx context.Context, namespace, label string) ([]stack.PodDetail, error) {
	pods, err := a.client.ListPodsByLabel(ctx, namespace, label)
	if err != nil {
		return nil, err
	}
	details := make([]stack.PodDetail, len(pods))
	for i, p := range pods {
		details[i] = stack.PodDetail{
			Name:    p.Name,
			Phase:   p.Phase,
			Ready:   p.Ready,
			Message: p.Message,
		}
	}
	return details, nil
}

// proxyBackendAdapter bridges proxy.Server to openclaw.BackendLister.
type proxyBackendAdapter struct{ s *proxy.Server }

func (a proxyBackendAdapter) ListBackends() map[string]*openclaw.Backend {
	pbs := a.s.ListBackends()
	result := make(map[string]*openclaw.Backend, len(pbs))
	for k, b := range pbs {
		result[k] = &openclaw.Backend{
			ModelName:           b.ModelName,
			EngineType:          b.EngineType,
			Address:             b.Address,
			Ready:               b.Ready,
			Remote:              b.Remote,
			ContextWindowTokens: b.ContextWindowTokens,
		}
	}
	return result
}

// catalogAdapter bridges knowledge.Catalog to openclaw.CatalogReader.
type catalogAdapter struct{ cat *knowledge.Catalog }

func (a catalogAdapter) ModelType(name string) string {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name == name {
			return m.Metadata.Type
		}
	}
	return ""
}

func (a catalogAdapter) ModelContextWindow(name string) int {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name != name {
			continue
		}
		for _, v := range m.Variants {
			if ml, ok := v.DefaultConfig["max_model_len"]; ok {
				switch n := ml.(type) {
				case int:
					return n
				case float64:
					return int(n)
				}
			}
		}
	}
	return 0
}

func (a catalogAdapter) ModelFamily(name string) string {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name == name {
			return m.Metadata.Family
		}
	}
	return ""
}

func (a catalogAdapter) ModelChatProvider(name string) bool {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name == name {
			if m.OpenClaw != nil && m.OpenClaw.ChatProvider != nil {
				return *m.OpenClaw.ChatProvider
			}
			return true // default: register as chat provider
		}
	}
	return true
}

func (a catalogAdapter) OpenClawRequestPatches(name string) []openclaw.RequestPatch {
	for _, m := range a.cat.ModelAssets {
		if m.Metadata.Name != name || m.OpenClaw == nil {
			continue
		}
		out := make([]openclaw.RequestPatch, 0, len(m.OpenClaw.RequestPatches))
		for _, patch := range m.OpenClaw.RequestPatches {
			out = append(out, openclaw.RequestPatch{
				Path:           patch.Path,
				EnginePrefixes: append([]string(nil), patch.EnginePrefixes...),
				Body:           patch.Body,
			})
		}
		return out
	}
	return nil
}

// Ensure adapters satisfy their interfaces at compile time.
var _ fleet.MCPExecutor = (*fleetMCPAdapter)(nil)
var _ stack.PodQuerier = (*podQuerierAdapter)(nil)
